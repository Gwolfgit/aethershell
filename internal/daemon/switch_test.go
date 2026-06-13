package daemon

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/Gwolfgit/aethershell/internal/proto"
)

// A "switch" request naming session A's tty must direct A's attached client to
// attach session B in place — the daemon emits a FrameSwitch to A's stream. This
// is the mechanism that lets in-session navigation reuse the terminal instead of
// nesting a second client.
func TestSwitchInPlaceDirectsClientToTarget(t *testing.T) {
	srv := NewServer("")
	t.Cleanup(func() {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		for _, s := range srv.sessions {
			s.Kill()
		}
	})

	// Client attached to session A.
	aConn, aName := attachNoDrain(t, srv, proto.Request{Type: "create", ClientID: "A", Rows: 24, Cols: 80})
	defer aConn.conn.Close()

	srv.mu.Lock()
	aSess := srv.sessions[aName]
	srv.mu.Unlock()
	if aSess == nil {
		t.Fatalf("session %q missing", aName)
	}
	pts := aSess.PtsName()
	if pts == "" {
		t.Skip("no pts available for the session pty in this environment")
	}

	// Session B exists (drained in the background to stay healthy).
	bConn, bName := attachNoDrain(t, srv, proto.Request{Type: "create", ClientID: "B", Rows: 24, Cols: 80})
	defer bConn.conn.Close()
	go drainTestFrames(bConn.br)

	// Watch A's stream for a FrameSwitch.
	switchCh := make(chan proto.SwitchTarget, 1)
	go func() {
		for {
			typ, payload, err := proto.ReadFrame(aConn.br)
			if err != nil {
				return
			}
			if typ == proto.FrameSwitch {
				var st proto.SwitchTarget
				_ = json.Unmarshal(payload, &st)
				switchCh <- st
				return
			}
		}
	}()

	// Issue a switch request from a third connection: A's tty → B.
	sClient, sServer := net.Pipe()
	defer sClient.Close()
	go srv.handleConn(sServer)
	if err := json.NewEncoder(sClient).Encode(proto.Request{Type: "switch", FromTTY: pts, Target: bName}); err != nil {
		t.Fatalf("send switch: %v", err)
	}
	sBr := bufio.NewReader(sClient)
	sLine, err := readLineWithTimeout(sBr, 2*time.Second)
	if err != nil {
		t.Fatalf("read switch response: %v", err)
	}
	var sResp proto.Response
	if err := json.Unmarshal(sLine, &sResp); err != nil {
		t.Fatalf("parse switch response: %v", err)
	}
	if sResp.Type != "ok" {
		t.Fatalf("switch failed: %+v", sResp)
	}

	select {
	case st := <-switchCh:
		if st.Name != bName {
			t.Fatalf("switch directed client to %q, want %q", st.Name, bName)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client A never received a FrameSwitch directive")
	}
}

// A switch request for a tty with no managed session must be rejected, not
// silently create or mis-route.
func TestSwitchUnknownTTYRejected(t *testing.T) {
	srv := NewServer("")
	sClient, sServer := net.Pipe()
	defer sClient.Close()
	go srv.handleConn(sServer)
	if err := json.NewEncoder(sClient).Encode(proto.Request{Type: "switch", FromTTY: "pts/9999", Target: "whatever"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	sBr := bufio.NewReader(sClient)
	line, err := readLineWithTimeout(sBr, 2*time.Second)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp proto.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Type != "error" {
		t.Fatalf("expected error for unknown tty, got %+v", resp)
	}
}

type testConn struct {
	conn net.Conn
	br   *bufio.Reader
}

// attachNoDrain attaches like openTestAttach but hands back the reader so the
// caller can inspect frames itself.
func attachNoDrain(t *testing.T, srv *Server, req proto.Request) (testConn, string) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	go srv.handleConn(serverConn)
	if err := json.NewEncoder(clientConn).Encode(req); err != nil {
		t.Fatalf("send request: %v", err)
	}
	br := bufio.NewReader(clientConn)
	line, err := readLineWithTimeout(br, 2*time.Second)
	if err != nil {
		clientConn.Close()
		t.Fatalf("read response: %v", err)
	}
	var resp proto.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		clientConn.Close()
		t.Fatalf("parse response: %v", err)
	}
	if resp.Type == "error" || resp.Session == nil {
		clientConn.Close()
		t.Fatalf("attach failed: %+v", resp)
	}
	return testConn{conn: clientConn, br: br}, resp.Session.Name
}
