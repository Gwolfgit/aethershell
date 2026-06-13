package daemon

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/Gwolfgit/aethershell/internal/proto"
)

type testAttach struct {
	conn net.Conn
	name string
}

func TestClientAffinityReattachesOriginalSessions(t *testing.T) {
	srv := NewServer("")
	t.Cleanup(func() {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		for _, sess := range srv.sessions {
			sess.Kill()
		}
	})

	clientIDs := []string{"client-a", "client-b", "client-c"}
	attached := make([]testAttach, 0, len(clientIDs))
	for _, clientID := range clientIDs {
		conn, name := openTestAttach(t, srv, proto.Request{
			Type:     "create",
			ClientID: clientID,
			Rows:     24,
			Cols:     80,
		})
		attached = append(attached, testAttach{conn: conn, name: name})
	}

	seen := make(map[string]bool)
	for _, a := range attached {
		if seen[a.name] {
			t.Fatalf("duplicate session name created: %s", a.name)
		}
		seen[a.name] = true
	}

	for i, clientID := range clientIDs {
		conn, name := openTestAttach(t, srv, proto.Request{
			Type:     "attach_client",
			ClientID: clientID,
			Rows:     24,
			Cols:     80,
		})
		if name != attached[i].name {
			t.Fatalf("client %s reattached to %s, want %s", clientID, name, attached[i].name)
		}
		attached[i].conn.Close()
		attached[i].conn = conn
	}

	srv.mu.Lock()
	if len(srv.sessions) != 3 {
		t.Fatalf("session count = %d, want 3", len(srv.sessions))
	}
	for i, clientID := range clientIDs {
		if got := srv.affinity[clientID]; got != attached[i].name {
			t.Fatalf("affinity[%s] = %s, want %s", clientID, got, attached[i].name)
		}
	}
	srv.mu.Unlock()
}

func TestAttachRejectsBusySessionUnlessForced(t *testing.T) {
	srv := NewServer("")
	t.Cleanup(func() {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		for _, sess := range srv.sessions {
			sess.Kill()
		}
	})

	ownerConn, name := openTestAttach(t, srv, proto.Request{
		Type:     "create",
		ClientID: "owner",
		Rows:     24,
		Cols:     80,
	})
	defer ownerConn.Close()

	expectTestAttachError(t, srv, proto.Request{
		Type:     "attach",
		Name:     name,
		ClientID: "other",
		Rows:     24,
		Cols:     80,
	})

	takeoverConn, takeoverName := openTestAttach(t, srv, proto.Request{
		Type:     "attach",
		Name:     name,
		ClientID: "other",
		Force:    true,
		Rows:     24,
		Cols:     80,
	})
	defer takeoverConn.Close()
	if takeoverName != name {
		t.Fatalf("forced attach got %s, want %s", takeoverName, name)
	}
}

func openTestAttach(t *testing.T, srv *Server, req proto.Request) (net.Conn, string) {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	go srv.handleConn(serverConn)

	if err := json.NewEncoder(clientConn).Encode(req); err != nil {
		t.Fatalf("send request: %v", err)
	}
	br := bufio.NewReader(clientConn)
	line, err := readLineWithTimeout(br, time.Second)
	if err != nil {
		clientConn.Close()
		t.Fatalf("read response: %v", err)
	}

	var resp proto.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		clientConn.Close()
		t.Fatalf("parse response: %v", err)
	}
	if resp.Type == "error" {
		clientConn.Close()
		t.Fatalf("daemon error: %s", resp.Error)
	}
	if resp.Session == nil || resp.Session.Name == "" {
		clientConn.Close()
		t.Fatalf("missing attached session in response: %+v", resp)
	}

	go drainTestFrames(br)
	return clientConn, resp.Session.Name
}

func expectTestAttachError(t *testing.T, srv *Server, req proto.Request) {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	go srv.handleConn(serverConn)

	if err := json.NewEncoder(clientConn).Encode(req); err != nil {
		t.Fatalf("send request: %v", err)
	}
	br := bufio.NewReader(clientConn)
	line, err := readLineWithTimeout(br, time.Second)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	var resp proto.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp.Type != "error" {
		t.Fatalf("expected error response, got %+v", resp)
	}
}

func readLineWithTimeout(br *bufio.Reader, timeout time.Duration) ([]byte, error) {
	type result struct {
		line []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := br.ReadBytes('\n')
		ch <- result{line: line, err: err}
	}()
	select {
	case r := <-ch:
		return r.line, r.err
	case <-time.After(timeout):
		return nil, errTimeout
	}
}

func drainTestFrames(br *bufio.Reader) {
	for {
		if _, _, err := proto.ReadFrame(br); err != nil {
			return
		}
	}
}

type timeoutError struct{}

func (timeoutError) Error() string { return "timeout" }

var errTimeout timeoutError
