// Package client implements the aether client: connects to the daemon,
// lists sessions, creates/attaches to sessions, and handles terminal I/O.
package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/Gwolfgit/aethershell/internal/proto"
)

// Client connects to the aether daemon over a Unix socket.
type Client struct {
	socketPath string
}

// NewClient creates a client that will connect to the given socket.
func NewClient(socketPath string) *Client {
	return &Client{socketPath: socketPath}
}

// ListSessions fetches the list of sessions from the daemon.
func (c *Client) ListSessions() ([]proto.Session, error) {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer conn.Close()

	// Send list request
	if err := json.NewEncoder(conn).Encode(proto.Request{Type: "list"}); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	// Read response
	var resp proto.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.Type == "error" {
		return nil, fmt.Errorf("daemon error: %s", resp.Error)
	}

	return resp.Sessions, nil
}

// CreateSession creates a new session and attaches to it.
// On success, the caller's terminal is set to raw mode and PTY I/O begins.
func (c *Client) CreateSession() error {
	return c.doAttach(proto.Request{Type: "create"})
}

// CreateSessionForClient creates a new session associated with a stable client
// identity and attaches to it.
func (c *Client) CreateSessionForClient(clientID string) error {
	return c.doAttach(proto.Request{Type: "create", ClientID: clientID})
}

// AttachSession attaches to an existing session by name.
func (c *Client) AttachSession(name string) error {
	return c.doAttach(proto.Request{Type: "attach", Name: name})
}

// AttachSessionForClient attaches to an existing session and records affinity
// for future reconnects by this client.
func (c *Client) AttachSessionForClient(name, clientID string) error {
	return c.doAttach(proto.Request{Type: "attach", Name: name, ClientID: clientID})
}

// AttachSessionTakeover attaches to an existing session even if the daemon
// still considers it in use.
func (c *Client) AttachSessionTakeover(name string) error {
	return c.doAttach(proto.Request{Type: "attach", Name: name, Force: true})
}

// AttachSessionTakeoverForClient attaches to an in-use session and records
// client affinity for future reconnects.
func (c *Client) AttachSessionTakeoverForClient(name, clientID string) error {
	return c.doAttach(proto.Request{Type: "attach", Name: name, ClientID: clientID, Force: true})
}

// AttachClientSession reattaches to the session mapped to a stable client ID.
func (c *Client) AttachClientSession(clientID string) error {
	return c.doAttach(proto.Request{Type: "attach_client", ClientID: clientID})
}

// KillSession destroys a session.
func (c *Client) KillSession(name string) error {
	return c.sendSimple("kill", name)
}

// RestartSession restarts the shell in a session.
func (c *Client) RestartSession(name string) error {
	return c.sendSimple("restart", name)
}

// KillAllSessions destroys all sessions.
func (c *Client) KillAllSessions() error {
	return c.sendSimple("kill_all", "")
}

// RestartAllSessions restarts all sessions.
func (c *Client) RestartAllSessions() error {
	return c.sendSimple("restart_all", "")
}

// UpgradeDaemon asks a running daemon to exec the current aetherd binary while
// handing off its listener and PTY file descriptors so sessions survive.
func (c *Client) UpgradeDaemon() error {
	return c.sendSimple("upgrade", "")
}

func (c *Client) sendSimple(reqType, name string) error {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(proto.Request{Type: reqType, Name: name}); err != nil {
		return fmt.Errorf("send request: %w", err)
	}

	var resp proto.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.Type == "error" {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// doAttach connects to the daemon, sends a create/attach request, then runs the
// framed data stream: the daemon replays scrollback and streams live PTY output
// as data frames, while keystrokes/resize/detach travel back as frames. The
// daemon stays in the data path (no fd passing) so it can buffer scrollback.
func (c *Client) doAttach(req proto.Request) error {
	geo := CurrentGeometry()
	req.Rows = geo.Rows
	req.Cols = geo.Cols
	req.XPixel = geo.XPixel
	req.YPixel = geo.YPixel

	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}
	defer conn.Close()

	// Send request (json.Encoder appends a newline the daemon reads as the
	// request line delimiter).
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fmt.Errorf("send request: %w", err)
	}

	// Read the newline-delimited handshake response, leaving subsequent frame
	// bytes buffered in br.
	br := bufio.NewReader(conn)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	var resp proto.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	if resp.Type == "error" {
		return fmt.Errorf("%s", resp.Error)
	}

	// ---- Terminal raw mode ----
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("terminal raw mode: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	// Serialize all frame writes to the daemon (stdin + resize goroutines).
	var writeMu sync.Mutex
	writeFrame := func(typ byte, payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return proto.WriteFrame(conn, typ, payload)
	}
	sendResize := func(g Geometry) {
		ctrl := proto.Control{Type: "resize", Rows: g.Rows, Cols: g.Cols, XPixel: g.XPixel, YPixel: g.YPixel}
		if b, err := json.Marshal(ctrl); err == nil {
			_ = writeFrame(proto.FrameResize, b)
		}
	}

	// Forward SIGWINCH as resize frames.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			if g := CurrentGeometry(); g.valid() {
				sendResize(g)
			}
		}
	}()

	// Initial size so the shell matches this client immediately.
	if geo.valid() {
		sendResize(geo)
	}

	// Goroutine: stdin → data frames.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := os.Stdin.Read(buf)
			if n > 0 {
				if werr := writeFrame(proto.FrameData, buf[:n]); werr != nil {
					break
				}
			}
			if rerr != nil {
				break
			}
		}
		// stdin closed → detach and tear down the connection to unblock reads.
		_ = writeFrame(proto.FrameDetach, nil)
		conn.Close()
	}()

	// Main loop: data frames → stdout.
	started := time.Now()
	var written int64
	var copyErr error
	for {
		typ, payload, ferr := proto.ReadFrame(br)
		if ferr != nil {
			if ferr != io.EOF {
				copyErr = ferr
			}
			break
		}
		if typ == proto.FrameData && len(payload) > 0 {
			if _, werr := os.Stdout.Write(payload); werr != nil {
				copyErr = werr
				break
			}
			written += int64(len(payload))
		}
	}

	if copyErr != nil {
		return fmt.Errorf("session I/O: %w", copyErr)
	}
	if written == 0 && time.Since(started) < time.Second {
		return fmt.Errorf("session ended immediately")
	}
	return nil
}
