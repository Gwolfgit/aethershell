package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Gwolfgit/aethershell/internal/detect"
	"github.com/Gwolfgit/aethershell/internal/proto"
)

// Server is the aether daemon: manages PTY sessions and serves client
// connections over a Unix domain socket.
type Server struct {
	socketPath string
	listener   net.Listener

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
	// Ensure socket directory exists
	dir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
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

	for {
		conn, err := l.Accept()
		if err != nil {
			// Listener closed — shutting down
			return nil
		}
		go s.handleConn(conn)
	}
}

func (s *Server) shutdown() {
	s.listener.Close()
	os.Remove(s.socketPath)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		sess.Kill()
	}
}

// handleConn reads a newline-delimited JSON request from the client and routes
// it. For attach/create the same connection then carries the framed PTY data
// stream (see streamSession); a bufio.Reader is used so any bytes the client
// pipelines after the request line stay available to the frame parser.
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	br := bufio.NewReader(conn)

	// The client encodes its request with json.Encoder, which appends a newline.
	// Read exactly that line so all subsequent bytes remain buffered for frames.
	line, err := br.ReadBytes('\n')
	if err != nil {
		s.sendError(conn, "bad request: "+err.Error())
		return
	}
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
		s.handleCreate(conn, br, req.ClientID, geoFromReq(req))

	case "attach":
		s.handleAttach(conn, br, req.Name, req.ClientID, req.Force, geoFromReq(req))

	case "attach_client":
		s.handleAttachClient(conn, br, req.ClientID, geoFromReq(req))

	case "kill":
		s.handleKill(conn, req.Name)

	case "restart":
		s.handleRestart(conn, req.Name)

	case "kill_all":
		s.handleKillAll(conn)

	case "restart_all":
		s.handleRestartAll(conn)

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
	managedTTYs := make(map[string]bool, len(s.order))
	sessions := make([]proto.Session, 0, len(s.order))
	for _, name := range s.order {
		sess := s.sessions[name]
		fgPid := sess.ForegroundPID()
		agent := detect.Detect(fgPid)
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
func (s *Server) handleCreate(conn net.Conn, br *bufio.Reader, clientID string, geo Geometry) {
	clientID = cleanClientID(clientID)

	s.mu.Lock()
	name := s.uniqueSessionNameLocked()
	sess, err := NewSession(name, geo, clientID)
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
