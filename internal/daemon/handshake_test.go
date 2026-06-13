package daemon

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

// A connection that connects but never sends a request line must be dropped
// after handshakeTimeout rather than parking a handler goroutine forever.
func TestHandshakeDeadlineDropsSilentConnection(t *testing.T) {
	old := handshakeTimeout
	handshakeTimeout = 200 * time.Millisecond
	defer func() { handshakeTimeout = old }()

	dir := t.TempDir()
	sock := filepath.Join(dir, "sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	srv := NewServer(sock)
	done := make(chan struct{})
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		srv.handleConn(c) // blocks until the deadline fires, then returns
		close(done)
	}()

	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	// Send nothing at all.

	select {
	case <-done:
		// handleConn returned (dropped the connection) — good.
	case <-time.After(3 * time.Second):
		t.Fatal("handleConn did not return; handshake deadline not enforced")
	}

	// The server writes a "bad request" error and then closes. Drain until the
	// connection terminates; reaching an error (EOF) proves it was closed rather
	// than left hanging.
	client.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 256)
	closed := false
	for i := 0; i < 10; i++ {
		if _, err := client.Read(buf); err != nil {
			closed = true
			break
		}
	}
	if !closed {
		t.Fatal("connection was not closed by the server after handshake timeout")
	}
}
