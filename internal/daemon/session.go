package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// Session wraps a PTY-backed shell process.
type Session struct {
	Name         string
	Created      time.Time
	ClientID     string
	LastAttached time.Time
	cmd          *exec.Cmd
	pty          *os.File // PTY master fd; kept open so shell survives client detach
	shellPid     int
	hub          *outputHub // drains the PTY, keeps scrollback, fans out to clients

	mu       sync.Mutex
	active   *attachment // current client streaming this session, if any
	attachID int64
	geo      Geometry // last-known client terminal geometry
}

type attachment struct {
	id       int64
	clientID string
	takeover chan struct{}
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

// NewSession starts a shell in a new PTY and returns the session.
func NewSession(name string, geo Geometry, clientID string) (*Session, error) {
	geo = geo.normalize()

	cmd := exec.Command("/bin/bash")
	cmd.Env = append(os.Environ(),
		"AETHER_SESSION="+name,
		"TERM=xterm-256color",
	)
	if clientID != "" {
		cmd.Env = append(cmd.Env, "AETHERSHELL_CLIENT_ID="+clientID)
	}
	cmd.Env = append(cmd.Env, geo.envVars()...)

	f, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(geo.Rows), Cols: uint16(geo.Cols),
		X: uint16(geo.XPixel), Y: uint16(geo.YPixel),
	})
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}

	now := time.Now()
	sess := &Session{
		Name:         name,
		Created:      now,
		ClientID:     clientID,
		LastAttached: now,
		cmd:          cmd,
		pty:          f,
		shellPid:     cmd.Process.Pid,
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
	n, err := unix.IoctlGetInt(int(s.pty.Fd()), unix.TIOCGPTN)
	if err != nil {
		return ""
	}
	return "pts/" + fmt.Sprint(n)
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
	}
	s.active = a
	return a, nil
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
// The PTY master fd remains open; the old process is reaped and a fresh bash is spawned.
func (s *Session) RestartShell() error {
	// Open the new slave BEFORE killing the old shell so at least one slave fd
	// stays open the whole time. Otherwise the brief window with no slave open
	// makes the PTY master read return EIO, tearing down the drain loop.
	ptsNum, err := unix.IoctlGetInt(int(s.pty.Fd()), unix.TIOCGPTN)
	if err != nil {
		return fmt.Errorf("ioctl TIOCGPTN: %w", err)
	}
	slavePath := fmt.Sprintf("/dev/pts/%d", ptsNum)
	slave, err := os.OpenFile(slavePath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open pty slave %s: %w", slavePath, err)
	}
	defer slave.Close()

	// Kill the old shell
	s.killShell()

	// Start a new shell connected to the PTY slave
	cmd := exec.Command("/bin/bash")
	cmd.Env = append(os.Environ(),
		"AETHER_SESSION="+s.Name,
		"TERM=xterm-256color",
	)
	if s.ClientID != "" {
		cmd.Env = append(cmd.Env, "AETHERSHELL_CLIENT_ID="+s.ClientID)
	}
	cmd.Env = append(cmd.Env, s.geo.envVars()...)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    int(slave.Fd()),
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restart shell: %w", err)
	}
	s.cmd = cmd
	s.shellPid = cmd.Process.Pid
	return nil
}

func (s *Session) killShell() {
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Signal(syscall.SIGKILL)
		s.cmd.Process.Wait()
		return
	}
	if s.shellPid > 0 {
		_ = syscall.Kill(s.shellPid, syscall.SIGKILL)
	}
}
