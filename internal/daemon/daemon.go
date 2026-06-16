package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Gwolfgit/aethershell/internal/detect"
	"github.com/Gwolfgit/aethershell/internal/proto"
	"github.com/Gwolfgit/aethershell/internal/sessiontitle"
	"golang.org/x/sys/unix"
)

// handshakeTimeout bounds how long a connection may take to send its initial
// JSON request line. Without it, a client that connects and never sends a
// newline parks a handler goroutine (and an fd) indefinitely; many such
// connections exhaust the daemon. The deadline is cleared once a stream is
// established, since attach connections are intentionally long-lived.
// It is a var only so tests can shorten it.
var handshakeTimeout = 15 * time.Second

// maxConcurrentConns bounds in-flight connection handlers so a flood of
// half-open connections cannot exhaust goroutines/fds. Legitimate use (a
// handful of attached terminals plus the occasional list/management call) is
// far below this.
const maxConcurrentConns = 256

// maxSessions caps the total number of live aether-managed sessions the daemon
// will hold. Reattaching to an existing session never counts against this — only
// fresh `create` requests do — so legitimate bursts (many terminals reconnecting
// at once after a network change) are unaffected. The ceiling exists to stop a
// pathological create-storm (e.g. a broken transport reconnecting and creating a
// new session every few seconds) from accumulating thousands of detached shells.
//
// maxSessionsPerClient caps how many live sessions a single client id may own.
// One workstation opening a handful of terminals stays far below this; the cap
// only trips on a runaway client minting sessions in a loop.
//
// Both are vars (not consts) only so tests can lower them.
var (
	maxSessions          = 128
	maxSessionsPerClient = 16
)

// Server is the aether daemon: manages PTY sessions and serves client
// connections over a Unix domain socket.
type Server struct {
	socketPath string

	connCount int32 // atomic: in-flight handleConn count

	lnMu      sync.Mutex
	listener  net.Listener
	socketIno uint64
	closing   bool

	mu       sync.Mutex
	sessions map[string]*Session // name → session
	order    []string            // creation order
	affinity map[string]string   // client id → session name
}

// NewServer creates a daemon server bound to the given socket path.
func NewServer(socketPath string) *Server {
	return &Server{
		socketPath: socketPath,
		sessions:   make(map[string]*Session),
		affinity:   make(map[string]string),
	}
}

// Start begins listening and accepting connections. Blocks until the server
// shuts down (via SIGTERM/SIGINT, or when the socket is removed).
func (s *Server) Start() error {
	// Ensure socket directory exists and is private. MkdirAll only applies the
	// mode to directories it creates, so chmod an existing dir too: the 0700
	// parent is what protects the socket during the unavoidable window between
	// net.Listen creating it and the Chmod below tightening it to 0600.
	dir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("chmod dir %s: %w", dir, err)
	}

	// Remove stale socket
	os.Remove(s.socketPath)

	l, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	s.listener = l

	// Set restrictive permissions on the socket so only this user can connect
	if err := os.Chmod(s.socketPath, 0600); err != nil {
		l.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	return s.Serve(l)
}

// Serve accepts client connections on an already-created listener. This is used
// by hot restore after exec, where the listening socket fd is inherited.
func (s *Server) Serve(l net.Listener) error {
	s.setListener(l)
	s.recordSocketInode()
	log.Printf("aetherd listening on %s", s.socketPath)

	// Handle shutdown signals gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Printf("aetherd shutting down...")
		s.shutdown()
		os.Exit(0)
	}()

	// Guard the socket file. /run/user is a volatile tmpfs: if the socket file is
	// unlinked while the daemon runs, the kernel keeps the listener open but every
	// client connect() fails with ENOENT forever. The guard recreates it.
	go s.guardSocket()

	return s.acceptLoop()
}

// acceptLoop accepts connections off the current listener. If the socket guard
// swaps in a replacement listener (after the socket file vanished), it closes
// the old one; we notice the swap and keep serving on the new listener.
func (s *Server) acceptLoop() error {
	for {
		l := s.currentListener()
		conn, err := l.Accept()
		if err != nil {
			if s.isClosing() {
				return nil
			}
			// A guard re-listen closes the old listener to swap in a new one.
			// If the current listener changed, adopt it and keep serving;
			// otherwise this is a genuine fatal error.
			if s.currentListener() != l {
				continue
			}
			return nil
		}
		go s.handleConn(conn)
	}
}

func (s *Server) shutdown() {
	s.lnMu.Lock()
	s.closing = true
	l := s.listener
	s.lnMu.Unlock()
	if l != nil {
		l.Close()
	}
	os.Remove(s.socketPath)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		sess.Kill()
	}
}

// --- socket guard ---

func (s *Server) setListener(l net.Listener) {
	s.lnMu.Lock()
	s.listener = l
	s.lnMu.Unlock()
}

func (s *Server) currentListener() net.Listener {
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	return s.listener
}

func (s *Server) isClosing() bool {
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	return s.closing
}

// recordSocketInode remembers the inode of the socket file currently backing the
// listener, so the guard can tell "our socket is gone" from "another instance
// rebound the path".
func (s *Server) recordSocketInode() {
	_, ino := socketPresent(s.socketPath)
	s.lnMu.Lock()
	s.socketIno = ino
	s.lnMu.Unlock()
}

func (s *Server) expectedInode() uint64 {
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	return s.socketIno
}

// guardSocket periodically verifies the listening socket file still exists and
// still refers to this daemon's listener, recreating it if it was unlinked.
// Live sessions are unaffected — only the listener used for new connections is
// replaced.
func (s *Server) guardSocket() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if s.isClosing() {
			return
		}
		present, ino := socketPresent(s.socketPath)
		switch {
		case present && ino == s.expectedInode():
			// healthy
		case present:
			// Another instance rebound the path; don't fight over it.
			log.Printf("aetherd: socket %s replaced by another instance; stopping socket guard", s.socketPath)
			return
		default:
			log.Printf("aetherd: socket %s vanished; recreating", s.socketPath)
			if err := s.relisten(); err != nil {
				log.Printf("aetherd: recreate socket failed: %v", err)
			}
		}
	}
}

// relisten creates a fresh listener on the socket path and swaps it in for the
// stale one. Already-accepted connections (attached clients) keep working; only
// new connections use the replacement.
func (s *Server) relisten() error {
	dir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("chmod dir %s: %w", dir, err)
	}
	os.Remove(s.socketPath)

	l, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	if err := os.Chmod(s.socketPath, 0600); err != nil {
		l.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	s.lnMu.Lock()
	old := s.listener
	s.listener = l
	s.lnMu.Unlock()
	s.recordSocketInode()

	if old != nil {
		// The old listener is bound to the now-unlinked inode. Its default Close
		// would unlink the path — which now points at the socket we just created
		// — so disable that before closing. Closing unblocks acceptLoop, which
		// then adopts the new listener.
		if ul, ok := old.(*net.UnixListener); ok {
			ul.SetUnlinkOnClose(false)
		}
		old.Close()
	}
	log.Printf("aetherd: re-listening on %s", s.socketPath)
	return nil
}

// socketPresent reports whether the socket path exists and, if so, its inode.
func socketPresent(path string) (bool, uint64) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, 0
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return true, st.Ino
	}
	return true, 0
}

// handleConn reads a newline-delimited JSON request from the client and routes
// it. For attach/create the same connection then carries the framed PTY data
// stream (see streamSession); a bufio.Reader is used so any bytes the client
// pipelines after the request line stay available to the frame parser.
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	// Bound concurrent handlers so a half-open flood cannot exhaust the daemon.
	n := atomic.AddInt32(&s.connCount, 1)
	defer atomic.AddInt32(&s.connCount, -1)
	if n > maxConcurrentConns {
		log.Printf("aetherd: too many concurrent connections (%d); dropping", n)
		return
	}

	// Defense in depth: only this user's processes may talk to the daemon, even
	// if the socket/runtime-dir permissions are ever loosened.
	if !s.authorizePeer(conn) {
		return
	}

	br := bufio.NewReader(conn)

	// Bound the handshake: a connection that never sends a request line must not
	// pin a goroutine/fd forever.
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))

	// The client encodes its request with json.Encoder, which appends a newline.
	// Read exactly that line so all subsequent bytes remain buffered for frames.
	line, err := br.ReadBytes('\n')
	if err != nil {
		s.sendError(conn, "bad request: "+err.Error())
		return
	}

	// Request line received; clear the deadline so attach streams (which are
	// long-lived and idle between keystrokes) are not torn down.
	_ = conn.SetReadDeadline(time.Time{})
	var req proto.Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.sendError(conn, "bad request: "+err.Error())
		return
	}

	// Route to handler
	switch req.Type {
	case "list":
		s.handleList(conn)

	case "create":
		s.handleCreate(conn, br, req.ClientID, geoFromReq(req), req.Env)

	case "attach":
		s.handleAttach(conn, br, req.Name, req.ClientID, req.Force, geoFromReq(req))

	case "attach_client":
		s.handleAttachClient(conn, br, req.ClientID, geoFromReq(req))

	case "switch":
		s.handleSwitch(conn, req)

	case "kill":
		s.handleKill(conn, req.Name)

	case "restart":
		s.handleRestart(conn, req.Name)

	case "kill_all":
		s.handleKillAll(conn)

	case "restart_all":
		s.handleRestartAll(conn)

	case "upgrade":
		s.handleUpgrade(conn)

	case "detach":
		// Client is explicitly detaching; the stream is closed via a detach
		// frame on the attach connection, so nothing to do here.
		return

	default:
		s.sendError(conn, "unknown request type: "+req.Type)
	}
}

// handleList sends back the list of all sessions: aether-managed sessions
// first, then read-only external remote logins discovered via utmp/proc.
func (s *Server) handleList(conn net.Conn) {
	s.pruneDeadSessions()

	s.mu.Lock()
	hostname, _ := os.Hostname()
	managedTTYs := make(map[string]bool, len(s.order))
	sessions := make([]proto.Session, 0, len(s.order))
	for _, name := range append([]string(nil), s.order...) {
		sess := s.sessions[name]
		if sess == nil {
			continue
		}
		fgPid := sess.ForegroundPID()
		agent := detect.Detect(fgPid)
		if title := sessiontitle.FromInfo(agent, hostname); title != "" {
			s.renameSessionLocked(sess, title)
		}
		attached, lastAttached := sess.AttachmentState()
		if pts := sess.PtsName(); pts != "" {
			managedTTYs[pts] = true
		}
		sessions = append(sessions, proto.Session{
			Name:         sess.Name,
			Created:      sess.Created,
			LastAttached: lastAttached,
			Attached:     attached,
			Agent:        agent,
		})
	}
	s.mu.Unlock()

	// Augment with external remote logins (cannot be attached, shown read-only).
	sessions = append(sessions, DiscoverRemoteSessions(managedTTYs)...)

	resp := proto.Response{Type: "session_list", Sessions: sessions}
	json.NewEncoder(conn).Encode(resp)
}

// geoFromReq extracts terminal geometry from a request.
func geoFromReq(req proto.Request) Geometry {
	return Geometry{Rows: req.Rows, Cols: req.Cols, XPixel: req.XPixel, YPixel: req.YPixel}
}

// handleCreate creates a new session and streams it to the client.
func (s *Server) handleCreate(conn net.Conn, br *bufio.Reader, clientID string, geo Geometry, connEnv map[string]string) {
	clientID = cleanClientID(clientID)

	// Reclaim dead sessions before enforcing the ceilings, so a client that keeps
	// reconnecting after its shell exited is never blocked by its own corpses.
	s.pruneDeadSessions()

	s.mu.Lock()
	if len(s.sessions) >= maxSessions {
		s.mu.Unlock()
		log.Printf("aetherd: session ceiling reached (%d); refusing create", maxSessions)
		s.sendError(conn, fmt.Sprintf("session limit reached (%d); close some sessions and retry", maxSessions))
		return
	}
	if clientID != "" && s.clientSessionCountLocked(clientID) >= maxSessionsPerClient {
		s.mu.Unlock()
		log.Printf("aetherd: per-client session ceiling reached (%d); refusing create", maxSessionsPerClient)
		s.sendError(conn, fmt.Sprintf("per-client session limit reached (%d); close some sessions and retry", maxSessionsPerClient))
		return
	}
	name := s.uniqueSessionNameLocked()
	sess, err := NewSession(name, geo, clientID, connEnv)
	if err != nil {
		s.mu.Unlock()
		s.sendError(conn, "create session: "+err.Error())
		return
	}
	att, err := sess.ClaimAttachment(clientID, true)
	if err != nil {
		s.mu.Unlock()
		s.sendError(conn, "create session: "+err.Error())
		return
	}
	s.sessions[name] = sess
	s.order = append(s.order, name)
	if clientID != "" {
		s.affinity[clientID] = name
	}
	s.mu.Unlock()

	s.streamSession(conn, br, sess, att)
}

// clientSessionCountLocked returns how many live sessions are owned by clientID.
// Callers must hold s.mu.
func (s *Server) clientSessionCountLocked(clientID string) int {
	n := 0
	for _, sess := range s.sessions {
		if sess != nil && sess.ClientID == clientID {
			n++
		}
	}
	return n
}

func (s *Server) uniqueSessionNameLocked() string {
	base := fmt.Sprintf("shell-%s", time.Now().Format("20060102-150405"))
	name := base
	for i := 2; s.sessions[name] != nil; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

// handleAttach attaches to an existing session and streams it to the client.
func (s *Server) handleAttach(conn net.Conn, br *bufio.Reader, name, clientID string, force bool, geo Geometry) {
	s.pruneDeadSessions()
	clientID = cleanClientID(clientID)

	s.mu.Lock()
	sess, ok := s.sessions[name]
	if !ok {
		s.mu.Unlock()
		s.sendError(conn, "session not found: "+name)
		return
	}
	att, err := sess.ClaimAttachment(clientID, force)
	if err != nil {
		s.mu.Unlock()
		s.sendError(conn, "session already in use: "+name)
		return
	}
	sess.Resize(geo)
	if clientID != "" {
		sess.ClientID = clientID
		s.affinity[clientID] = name
	}
	s.mu.Unlock()

	s.streamSession(conn, br, sess, att)
}

// handleAttachClient reattaches a client to the session previously associated
// with its stable client ID. It is intentionally allowed to supersede an
// existing attachment with the same client ID because stale SSH transports can
// remain visible to the daemon after a network transition.
func (s *Server) handleAttachClient(conn net.Conn, br *bufio.Reader, clientID string, geo Geometry) {
	s.pruneDeadSessions()
	clientID = cleanClientID(clientID)
	if clientID == "" {
		s.sendError(conn, "missing client id")
		return
	}

	s.mu.Lock()
	name, ok := s.affinity[clientID]
	if !ok {
		s.mu.Unlock()
		s.sendError(conn, "no session affinity for client")
		return
	}
	sess, ok := s.sessions[name]
	if !ok {
		delete(s.affinity, clientID)
		s.mu.Unlock()
		s.sendError(conn, "mapped session is gone")
		return
	}
	att, err := sess.ClaimAttachment(clientID, false)
	if err != nil {
		s.mu.Unlock()
		s.sendError(conn, "session already in use: "+name)
		return
	}
	sess.Resize(geo)
	s.mu.Unlock()

	s.streamSession(conn, br, sess, att)
}

// handleSwitch implements tmux-like switch-client. An in-session command running
// on tty req.FromTTY asks whichever client is currently viewing that session to
// attach a different session in place (req.Target), or a freshly created one
// (req.New), instead of nesting a second client inside the first. The current
// session is found by its controlling tty, so it survives session renames.
func (s *Server) handleSwitch(conn net.Conn, req proto.Request) {
	if req.FromTTY == "" {
		s.sendError(conn, "switch: missing source tty")
		return
	}

	s.mu.Lock()
	cur := s.findByTTYLocked(req.FromTTY)
	if cur == nil {
		s.mu.Unlock()
		s.sendError(conn, "switch: no managed session on "+req.FromTTY)
		return
	}

	var (
		targetName string
		created    *Session
	)
	if req.New {
		name := s.uniqueSessionNameLocked()
		sess, err := NewSession(name, geoFromReq(req), "", req.Env)
		if err != nil {
			s.mu.Unlock()
			s.sendError(conn, "switch: create session: "+err.Error())
			return
		}
		s.sessions[name] = sess
		s.order = append(s.order, name)
		targetName, created = name, sess
	} else {
		if req.Target == "" || s.sessions[req.Target] == nil {
			s.mu.Unlock()
			s.sendError(conn, "switch: target session not found: "+req.Target)
			return
		}
		targetName = req.Target
	}
	s.mu.Unlock()

	if !cur.RequestSwitch(proto.SwitchTarget{Name: targetName, Force: req.Force}) {
		// No live client to redirect; don't leave a freshly created session orphaned.
		if created != nil {
			s.mu.Lock()
			created.Kill()
			delete(s.sessions, targetName)
			s.order = removeStr(s.order, targetName)
			s.mu.Unlock()
		}
		s.sendError(conn, "switch: current session has no attached client")
		return
	}
	json.NewEncoder(conn).Encode(proto.Response{Type: "ok"})
}

// findByTTYLocked returns the managed session whose PTY slave is the given tty
// (e.g. "pts/7"), or nil. Caller must hold s.mu.
func (s *Server) findByTTYLocked(tty string) *Session {
	for _, name := range s.order {
		sess := s.sessions[name]
		if sess != nil && sess.PtsName() == tty {
			return sess
		}
	}
	return nil
}

// handleKill destroys a session.
func (s *Server) handleKill(conn net.Conn, name string) {
	s.mu.Lock()
	sess, ok := s.sessions[name]
	if !ok {
		s.mu.Unlock()
		s.sendError(conn, "session not found: "+name)
		return
	}
	sess.Kill()
	delete(s.sessions, name)
	s.order = removeStr(s.order, name)
	s.removeAffinityLocked(name)
	s.mu.Unlock()

	json.NewEncoder(conn).Encode(proto.Response{Type: "ok"})
}

// handleRestart restarts the shell in a session.
func (s *Server) handleRestart(conn net.Conn, name string) {
	s.mu.Lock()
	sess, ok := s.sessions[name]
	if !ok {
		s.mu.Unlock()
		s.sendError(conn, "session not found: "+name)
		return
	}
	err := sess.RestartShell()
	s.mu.Unlock()

	if err != nil {
		s.sendError(conn, err.Error())
		return
	}
	json.NewEncoder(conn).Encode(proto.Response{Type: "ok"})
}

// handleKillAll destroys all sessions.
func (s *Server) handleKillAll(conn net.Conn) {
	s.mu.Lock()
	for _, sess := range s.sessions {
		sess.Kill()
	}
	s.sessions = make(map[string]*Session)
	s.order = nil
	s.affinity = make(map[string]string)
	s.mu.Unlock()

	json.NewEncoder(conn).Encode(proto.Response{Type: "ok"})
}

// handleRestartAll restarts the shell in all sessions.
func (s *Server) handleRestartAll(conn net.Conn) {
	s.mu.Lock()
	var errs []string
	for _, sess := range s.sessions {
		if err := sess.RestartShell(); err != nil {
			errs = append(errs, sess.Name+": "+err.Error())
		}
	}
	s.mu.Unlock()

	if len(errs) > 0 {
		s.sendError(conn, strings.Join(errs, "; "))
		return
	}
	json.NewEncoder(conn).Encode(proto.Response{Type: "ok"})
}

// handleUpgrade replaces the running daemon process with the current aetherd
// binary while preserving the listener and all PTY master fds. Existing attach
// streams disconnect, but the shells remain alive and reconnect to the new
// daemon instance.
func (s *Server) handleUpgrade(conn net.Conn) {
	statePath, listenerFile, err := s.prepareUpgrade()
	if err != nil {
		s.sendError(conn, "prepare upgrade: "+err.Error())
		return
	}
	defer listenerFile.Close()
	// On a successful exec this process is replaced and the new daemon removes
	// the state file after reading it. These deferred cleanups only run if the
	// upgrade fails, so a failed upgrade does not leave session metadata
	// (names, client IDs) lingering on disk.
	defer os.Remove(statePath)

	exe, err := os.Executable()
	if err != nil {
		s.sendError(conn, "upgrade executable: "+err.Error())
		return
	}
	if _, err := exec.LookPath(exe); err != nil {
		s.sendError(conn, "upgrade executable: "+err.Error())
		return
	}

	if err := json.NewEncoder(conn).Encode(proto.Response{Type: "ok"}); err != nil {
		return
	}
	_ = conn.Close()

	argv := []string{exe, "--restore", statePath}
	env := append(os.Environ(), restoreFDEnv+"="+strconv.Itoa(int(listenerFile.Fd())))
	log.Printf("hot-upgrading daemon via exec: %s", exe)
	if err := syscall.Exec(exe, argv, env); err != nil {
		log.Printf("hot upgrade failed: %v", err)
	}
}

func (s *Server) prepareUpgrade() (string, *os.File, error) {
	fileProvider, ok := s.currentListener().(interface {
		File() (*os.File, error)
	})
	if !ok {
		return "", nil, fmt.Errorf("listener does not expose a file descriptor")
	}
	listenerFile, err := fileProvider.File()
	if err != nil {
		return "", nil, fmt.Errorf("listener fd: %w", err)
	}
	if err := clearCloseOnExec(int(listenerFile.Fd())); err != nil {
		listenerFile.Close()
		return "", nil, err
	}

	state := restoreState{
		SocketPath: s.socketPath,
		Affinity:   map[string]string{},
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for k, v := range s.affinity {
		state.Affinity[k] = v
	}
	state.Order = append([]string(nil), s.order...)

	for _, name := range s.order {
		sess := s.sessions[name]
		if sess == nil || !sess.IsAlive() {
			continue
		}
		if err := clearCloseOnExec(int(sess.pty.Fd())); err != nil {
			listenerFile.Close()
			return "", nil, fmt.Errorf("session %s pty fd: %w", name, err)
		}
		sess.mu.Lock()
		geo := sess.geo
		lastAttached := sess.LastAttached
		sess.mu.Unlock()
		state.Sessions = append(state.Sessions, restoreSession{
			Name:        sess.Name,
			CreatedUnix: sess.Created.UnixNano(),
			ClientID:    sess.ClientID,
			LastUnix:    lastAttached.UnixNano(),
			ShellPID:    sess.shellPid,
			ShellStart:  sess.shellStart,
			Geometry:    geo,
			PTYFD:       int(sess.pty.Fd()),
		})
	}

	data, err := json.Marshal(state)
	if err != nil {
		listenerFile.Close()
		return "", nil, err
	}
	statePath := filepath.Join(filepath.Dir(s.socketPath), fmt.Sprintf("restore-%d.json", os.Getpid()))
	if err := os.WriteFile(statePath, data, 0600); err != nil {
		listenerFile.Close()
		return "", nil, err
	}
	return statePath, listenerFile, nil
}

func clearCloseOnExec(fd int) error {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		return fmt.Errorf("fcntl getfd: %w", err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags&^unix.FD_CLOEXEC); err != nil {
		return fmt.Errorf("fcntl setfd: %w", err)
	}
	return nil
}

// streamSession is the data path. It sends the "attached" handshake, then:
//   - replays the session's scrollback so a (re)attaching client sees the
//     history that was last on screen, then streams live PTY output to the
//     client as data frames;
//   - reads framed input from the client (keystrokes, resize, detach) and
//     applies it to the session.
//
// It returns when the client disconnects/detaches or the shell exits.
func (s *Server) streamSession(conn net.Conn, br *bufio.Reader, sess *Session, att *attachment) {
	defer sess.ReleaseAttachment(att)

	s.refreshSessionTitle(sess)
	_, lastAttached := sess.AttachmentState()
	resp := proto.Response{
		Type:    "attached",
		Session: &proto.Session{Name: sess.Name, Created: sess.Created, LastAttached: lastAttached},
	}
	respBytes, _ := json.Marshal(resp)
	respBytes = append(respBytes, '\n')
	if _, err := conn.Write(respBytes); err != nil {
		return
	}

	sub, snapshot := sess.Subscribe()
	defer sess.Unsubscribe(sub)

	// Reader goroutine: client → PTY. Closes `done` when the client goes away so
	// the writer loop below can return.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			typ, payload, err := proto.ReadFrame(br)
			if err != nil {
				return
			}
			switch typ {
			case proto.FrameData:
				sess.Write(payload)
			case proto.FrameResize:
				var c proto.Control
				if json.Unmarshal(payload, &c) == nil && c.Rows > 0 && c.Cols > 0 {
					sess.Resize(Geometry{Rows: c.Rows, Cols: c.Cols, XPixel: c.XPixel, YPixel: c.YPixel})
				}
			case proto.FrameDetach:
				return
			}
		}
	}()

	log.Printf("client attached to session %q (replaying %d bytes scrollback)", sess.Name, len(snapshot))

	// Writer: scrollback replay, then live output → client.
	if len(snapshot) > 0 {
		if err := proto.WriteFrame(conn, proto.FrameData, snapshot); err != nil {
			return
		}
	}

	lastTitle := ""
	writeTitle := func(force bool) bool {
		title := s.refreshSessionTitle(sess)
		if title == "" || (!force && title == lastTitle) {
			return true
		}
		lastTitle = title
		osc := sessiontitle.OSC(title)
		if len(osc) == 0 {
			return true
		}
		return proto.WriteFrame(conn, proto.FrameData, osc) == nil
	}
	if !writeTitle(true) {
		return
	}

	titleTick := time.NewTicker(2 * time.Second)
	defer titleTick.Stop()

	for {
		select {
		case b, ok := <-sub.data:
			if !ok { // hub closed: shell exited / session killed
				return
			}
			if err := proto.WriteFrame(conn, proto.FrameData, b); err != nil {
				return
			}
		case <-done: // client detached or disconnected
			return
		case <-att.takeover:
			return
		case target := <-att.switchTo:
			// In-session switch-client: tell the client to attach `target` in
			// place of this session, then end the stream.
			if b, err := json.Marshal(target); err == nil {
				_ = proto.WriteFrame(conn, proto.FrameSwitch, b)
			}
			return
		case <-titleTick.C:
			if !writeTitle(false) {
				return
			}
		}
	}
}

func (s *Server) pruneDeadSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, name := range append([]string(nil), s.order...) {
		sess := s.sessions[name]
		if sess == nil || sess.IsAlive() {
			continue
		}
		log.Printf("removing dead session %q", name)
		sess.Kill()
		delete(s.sessions, name)
		s.order = removeStr(s.order, name)
		s.removeAffinityLocked(name)
	}
}

// --- helpers ---

func (s *Server) sendError(conn net.Conn, msg string) {
	json.NewEncoder(conn).Encode(proto.Response{Type: "error", Error: msg})
}

func removeStr(slice []string, s string) []string {
	for i, v := range slice {
		if v == s {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

func cleanClientID(clientID string) string {
	clientID = strings.TrimSpace(clientID)
	if len(clientID) > 128 {
		return ""
	}
	return clientID
}

func (s *Server) removeAffinityLocked(sessionName string) {
	for clientID, name := range s.affinity {
		if name == sessionName {
			delete(s.affinity, clientID)
		}
	}
}

func (s *Server) refreshSessionTitle(sess *Session) string {
	agent := detect.Detect(sess.ForegroundPID())
	hostname, _ := os.Hostname()
	title := sessiontitle.FromInfo(agent, hostname)
	if title == "" {
		return ""
	}
	s.mu.Lock()
	s.renameSessionLocked(sess, title)
	s.mu.Unlock()
	return title
}

func (s *Server) renameSessionLocked(sess *Session, desired string) {
	// Strip any control bytes before the title becomes the session name: the
	// name is sent to clients and rendered to terminals, and titles are derived
	// from untrusted cwd/cmdline data.
	desired = strings.TrimSpace(proto.SanitizeTerminal(desired))
	if desired == "" || sess == nil || sess.Name == desired {
		return
	}

	old := sess.Name
	name := s.uniqueRenamedSessionNameLocked(desired, sess)
	if old == name {
		return
	}

	delete(s.sessions, old)
	s.sessions[name] = sess
	sess.Name = name

	for i, n := range s.order {
		if n == old {
			s.order[i] = name
			break
		}
	}
	for clientID, mapped := range s.affinity {
		if mapped == old {
			s.affinity[clientID] = name
		}
	}
}

func (s *Server) uniqueRenamedSessionNameLocked(base string, sess *Session) string {
	name := base
	for i := 2; ; i++ {
		existing := s.sessions[name]
		if existing == nil || existing == sess {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

// FreeSessions returns a list of session names that are not currently attached.
func (s *Server) FreeSessions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var free []string
	for _, name := range s.order {
		if !s.sessions[name].IsAttached() {
			free = append(free, name)
		}
	}
	sort.Strings(free)
	return free
}
