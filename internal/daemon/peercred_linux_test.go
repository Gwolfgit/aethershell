package daemon

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// peerUID over a real Unix-domain socket must report the connecting process's
// uid, and authorizePeer must accept our own uid.
func TestPeerUIDReportsOwnUID(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			accepted <- c
		}
	}()

	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	srvConn := <-accepted
	defer srvConn.Close()

	uid, err := peerUID(srvConn)
	if err != nil {
		t.Fatalf("peerUID: %v", err)
	}
	if uid != uint32(os.Getuid()) {
		t.Fatalf("peerUID = %d, want our uid %d", uid, os.Getuid())
	}

	srv := NewServer(sock)
	if !srv.authorizePeer(srvConn) {
		t.Fatalf("authorizePeer rejected our own uid")
	}
}

// authorizePeer must allow in-process pipes (no kernel peer credential) so the
// transport-agnostic test harness keeps working; the real Unix socket always
// carries SO_PEERCRED and is enforced above.
func TestAuthorizePeerAllowsPipe(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	if _, err := peerUID(a); err != errNoPeerCred {
		t.Fatalf("peerUID on pipe = %v, want errNoPeerCred", err)
	}
	srv := NewServer("")
	if !srv.authorizePeer(a) {
		t.Fatalf("authorizePeer rejected an in-process pipe")
	}
}
