package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Gwolfgit/aethershell/internal/proto"
	"github.com/creack/pty"
)

// Session wraps a PTY-backed shell process.
type Session struct {
	Name         string
	Created      time.Time
	ClientID     string
	LastAttached time.Time
	cmd          *exec.Cmd
	pty          *os.File // PTY master fd; kept open so shell survives client detach
	slaveName    string   // slave tty path, e.g. /dev/pts/5 or /dev/ttys003
	shellPid     int
	shellStart   uint64            // /proc/<pid>/stat starttime; detects PID reuse after restore
	connEnv      map[string]string // forwarded connection env (TERM, LANG, LC_*, SSH_*); reused on restart
	hub          *outputHub        // drains the PTY, keeps scrollback, fans out to clients

	mu       sync.Mutex
	active   *attachment // current client streaming this session, if any
	attachID int64
	geo      Geometry // last-known client terminal geometry
}

type attachment struct {
	id       int64
	clientID string
	takeover chan struct{}
	// switchTo carries a tmux-like "switch-client" directive to the streaming
	// loop: when an in-session command asks to move this terminal to another
	// session, the target is delivered here. Buffered so the requester never
	// blocks. Read once, then the stream ends.
	switchTo chan proto.SwitchTarget
}

var ErrSessionAttached = errors.New("session already in use")

// Geometry describes the client's terminal geometry. Pixel fields are 0 when
// the terminal does not report them.
type Geometry struct {
	Rows, Cols, XPixel, YPixel int
}

func (g Geometry) normalize() Geometry {
	if g.Rows <= 0 {
		g.Rows = 24
	}
	if g.Cols <= 0 {
		g.Cols = 80
	}
	return g
}

// orientation derives a coarse landscape/portrait/square hint from pixels when
// available, otherwise from the character grid (assuming ~1:2 cell aspect).
func (g Geometry) orientation() string {
	w, h := g.XPixel, g.YPixel
	if w == 0 || h == 0 {
		// Approximate: a character cell is roughly half as wide as it is tall.
		w, h = g.Cols, g.Rows*2
	}
	switch {
	case w > h*5/4:
		return "landscape"
	case h > w*5/4:
		return "portrait"
	default:
		return "square"
	}
}

// envVars returns the geometry-derived environment to inject into the shell so
// it (and anything launched from it, including an onward `ssh`) sees the right
// size. AETHER_GEOMETRY is "cols rows xpixel ypixel orientation".
func (g Geometry) envVars() []string {
	g = g.normalize()
	return []string{
		"COLUMNS=" + fmt.Sprint(g.Cols),
		"LINES=" + fmt.Sprint(g.Rows),
		fmt.Sprintf("AETHER_GEOMETRY=%d %d %d %d %s", g.Cols, g.Rows, g.XPixel, g.YPixel, g.orientation()),
	}
}

// buildSessionEnv composes the environment for a session's shell. The base is
// the daemon's (i.e. the user account's) environment for PATH/HOME/USER/SHELL;
// the forwarded connection environment is overlaid on top so the shell sees the
// terminal type, locale, and connection identity the SSH layer actually
// negotiated for this login. TERM falls back to a sane default only when the
// connection did not supply one. aether's own session markers and the geometry
// are applied last so they always win.
func buildSessionEnv(name, clientID string, geo Geometry, connEnv map[string]string) []string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			env[kv[:eq]] = kv[eq+1:]
		}
	}
	for k, v := range connEnv {
		env[k] = v
	}
	if env["TERM"] == "" {
		env["TERM"] = "xterm-256color"
	}
	env["AETHER_SESSION"] = name
	if clientID != "" {
		env["AETHERSHELL_CLIENT_ID"] = clientID
	}
	for _, kv := range geo.envVars() {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			env[kv[:eq]] = kv[eq+1:]
		}
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out) // deterministic order (helps tests and logging)
	return out
}

func shellPath() string {
	if sh := os.Getenv("SHELL"); filepath.IsAbs(sh) {
		if st, err := os.Stat(sh); err == nil && !st.IsDir() {
			return sh
		}
	}
	for _, sh := range []string{"/bin/bash", "/usr/local/bin/bash", "/bin/sh"} {
		if st, err := os.Stat(sh); err == nil && !st.IsDir() {
			return sh
		}
	}
	return "/bin/sh"
}

func startShellInPTY(cmd *exec.Cmd, geo Geometry) (*os.File, string, error) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, "", err
	}
	defer tty.Close()

	if err := pty.Setsize(ptmx, &pty.Winsize{
		Rows: uint16(geo.Rows), Cols: uint16(geo.Cols),
		X: uint16(geo.XPixel), Y: uint16(geo.YPixel),
	}); err != nil {
		ptmx.Close()
		return nil, "", err
	}

	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	}

	if err := cmd.Start(); err != nil {
		ptmx.Close()
		return nil, "", err
	}
	return ptmx, tty.Name(), nil
}

// NewSession starts a shell in a new PTY and returns the session.
func NewSession(name string, geo Geometry, clientID string, connEnv map[string]string) (*Session, error) {
	geo = geo.normalize()

	cmd := exec.Command(shellPath())
	cmd.Env = buildSessionEnv(name, clientID, geo, connEnv)

	f, slaveName, err := startShellInPTY(cmd, geo)
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}

	now := time.Now()
	start, _ := procStartTicks(cmd.Process.Pid)
	sess := &Session{
		Name:         name,
		Created:      now,
		ClientID:     clientID,
		LastAttached: now,
		cmd:          cmd,
		pty:          f,
		slaveName:    slaveName,
		shellPid:     cmd.Process.Pid,
		shellStart:   start,
		connEnv:      connEnv,
		geo:          geo,
		hub:          newOutputHub(),
	}
	go sess.drain()
	return sess, nil
}

// drain continuously reads the PTY master into the scrollback ring and fans the
// bytes out to attached clients. It runs for the session's whole lifetime; when
// Read returns an error (shell exited or PTY closed) it shuts the hub down.
// Draining unconditionally also prevents a chatty background process from
// blocking on a full PTY buffer while no client is attached.
func (s *Session) drain() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			b := make([]byte, n)
			copy(b, buf[:n])
			s.hub.broadcast(b)
		}
		if err != nil {
			s.hub.close()
			return
		}
	}
}

// Subscribe registers a client to the live output stream and returns the
// subscriber plus a scrollback snapshot to replay before live bytes.
func (s *Session) Subscribe() (*subscriber, []byte) {
	return s.hub.subscribe()
}

// Unsubscribe detaches a client from the live output stream.
func (s *Session) Unsubscribe(sub *subscriber) {
	s.hub.unsubscribe(sub)
}

// Write sends client keystrokes to the shell via the PTY master.
func (s *Session) Write(b []byte) (int, error) {
	return s.pty.Write(b)
}

// Resize changes the PTY window size, including pixel dimensions when known.
func (s *Session) Resize(geo Geometry) error {
	if geo.Rows <= 0 || geo.Cols <= 0 {
		return nil
	}
	s.mu.Lock()
	s.geo = geo
	s.mu.Unlock()
	return pty.Setsize(s.pty, &pty.Winsize{
		Rows: uint16(geo.Rows), Cols: uint16(geo.Cols),
		X: uint16(geo.XPixel), Y: uint16(geo.YPixel),
	})
}

// PtsName returns the slave pseudo-terminal name (e.g. "pts/5") backing this
// session, or "" if it can't be determined. Used to exclude the daemon's own
// PTYs from external-session discovery.
func (s *Session) PtsName() string {
	if s.slaveName == "" && s.pty != nil {
		s.slaveName = ptySlaveName(s.pty)
	}
	return normalizeTTYName(s.slaveName)
}

// IsAlive reports whether the session's shell process is still usable.
func (s *Session) IsAlive() bool {
	pid := s.shellPid
	if pid <= 0 {
		if s.cmd == nil || s.cmd.Process == nil {
			return false
		}
		pid = s.cmd.Process.Pid
	}
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	// PID-reuse guard: if we recorded the shell's start-time, the live process
	// at this PID must still be that same instance. After a hot-upgrade a dead
	// shell's PID can be recycled by an unrelated process; treat that as dead so
	// we never adopt — or later kill — someone else's process.
	if s.shellStart != 0 {
		if start, ok := procStartTicks(pid); ok && start != s.shellStart {
			return false
		}
	}
	data, err := os.ReadFile("/proc/" + fmt.Sprint(pid) + "/stat")
	if err != nil {
		return true
	}
	fields := splitStat(string(data))
	if len(fields) < 3 {
		return true
	}
	return fields[2] != "Z"
}

// ClaimAttachment marks this session as owned by a client stream. A reconnect
// using the same client ID is allowed to replace its previous stream because
// the old SSH transport may be wedged and unable to detach promptly.
func (s *Session) ClaimAttachment(clientID string, force bool) (*attachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.active != nil {
		sameClient := clientID != "" && s.active.clientID == clientID
		if !force && !sameClient {
			return nil, ErrSessionAttached
		}
		close(s.active.takeover)
	}

	s.attachID++
	s.LastAttached = time.Now()
	a := &attachment{
		id:       s.attachID,
		clientID: clientID,
		takeover: make(chan struct{}),
		switchTo: make(chan proto.SwitchTarget, 1),
	}
	s.active = a
	return a, nil
}

// RequestSwitch asks the client currently streaming this session to switch in
// place to target. Returns false if no client is attached or a switch is
// already pending. Non-blocking.
func (s *Session) RequestSwitch(target proto.SwitchTarget) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		return false
	}
	select {
	case s.active.switchTo <- target:
		return true
	default:
		return false
	}
}

// ReleaseAttachment marks this session detached if the releasing stream is
// still the current owner. Superseded streams must not clear a newer owner.
func (s *Session) ReleaseAttachment(a *attachment) {
	s.mu.Lock()
	if s.active == a || (s.active != nil && a != nil && s.active.id == a.id) {
		s.active = nil
	}
	s.mu.Unlock()
}

// IsAttached returns whether a client currently holds this session.
func (s *Session) IsAttached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active != nil
}

func (s *Session) AttachmentState() (bool, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active != nil, s.LastAttached
}

// ForegroundPID returns the PID of the foreground process in the shell,
// obtained by reading /proc/<shellPid>/stat and following the tpgid.
// Returns 0 if it can't be determined.
func (s *Session) ForegroundPID() int {
	return foregroundPID(s.shellPid)
}

// Kill destroys the session: kills the shell process and closes the PTY.
func (s *Session) Kill() {
	s.killShell()
	s.pty.Close()
}

// RestartShell kills the current shell process inside the PTY and starts a new one.
// The PTY master fd remains open; the old process is reaped and a fresh shell is spawned.
func (s *Session) RestartShell() error {
	// Open the new slave BEFORE killing the old shell so at least one slave fd
	// stays open the whole time. Otherwise the brief window with no slave open
	// makes the PTY master read return EIO, tearing down the drain loop.
	slaveName := s.slaveName
	if slaveName == "" && s.pty != nil {
		slaveName = ptySlaveName(s.pty)
	}
	if slaveName == "" {
		return fmt.Errorf("pty slave name unavailable")
	}
	slave, err := os.OpenFile(slaveName, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open pty slave %s: %w", slaveName, err)
	}
	defer slave.Close()

	// Kill the old shell
	s.killShell()

	// Start a new shell connected to the PTY slave, reusing the same connection
	// environment the session was created with so a restart stays consistent.
	cmd := exec.Command(shellPath())
	cmd.Env = buildSessionEnv(s.Name, s.ClientID, s.geo, s.connEnv)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restart shell: %w", err)
	}
	s.cmd = cmd
	s.shellPid = cmd.Process.Pid
	s.shellStart, _ = procStartTicks(cmd.Process.Pid)
	s.slaveName = slaveName
	return nil
}

func (s *Session) killShell() {
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Signal(syscall.SIGKILL)
		s.cmd.Process.Wait()
		return
	}
	// Restored session (no os/exec handle): signal the raw PID, but only after
	// confirming it is still the same process we recorded. Without this check a
	// recycled PID belonging to an unrelated same-user process would be killed.
	if s.shellPid > 0 {
		if s.shellStart != 0 {
			if start, ok := procStartTicks(s.shellPid); !ok || start != s.shellStart {
				return
			}
		}
		_ = syscall.Kill(s.shellPid, syscall.SIGKILL)
	}
}
