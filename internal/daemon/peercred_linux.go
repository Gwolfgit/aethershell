package daemon

import (
	"errors"
	"log"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// errNoPeerCred means the connection has no kernel peer credential to check
// (e.g. an in-process net.Pipe used by tests, not a real Unix-domain socket).
var errNoPeerCred = errors.New("connection has no peer credentials")

// peerUID returns the uid of the process on the other end of a Unix-domain
// connection via SO_PEERCRED. It returns errNoPeerCred for connections that are
// not backed by a Unix-domain socket.
func peerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errNoPeerCred
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var ucred *unix.Ucred
	var sockErr error
	if cerr := raw.Control(func(fd uintptr) {
		ucred, sockErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); cerr != nil {
		return 0, cerr
	}
	if sockErr != nil {
		return 0, sockErr
	}
	if ucred == nil {
		return 0, errNoPeerCred
	}
	return ucred.Uid, nil
}

// authorizePeer enforces per-user isolation: only the daemon's own uid may issue
// requests. This is defense in depth behind the 0600 socket / 0700 runtime dir —
// it keeps sessions private even if the socket mode is loosened, the runtime
// directory is shared, or a future change weakens those permissions.
//
// Connections without a kernel peer credential (in-process pipes in tests) are
// necessarily same-process and are allowed; the only real transport is the Unix
// socket, which always carries SO_PEERCRED.
func (s *Server) authorizePeer(conn net.Conn) bool {
	uid, err := peerUID(conn)
	if err != nil {
		if errors.Is(err, errNoPeerCred) {
			return true
		}
		// Fail closed: if we cannot establish who the peer is, do not serve it.
		log.Printf("aetherd: rejecting connection: peer credential check failed: %v", err)
		return false
	}
	self := uint32(os.Getuid())
	if uid != self {
		log.Printf("aetherd: rejecting connection from uid %d (daemon runs as uid %d)", uid, self)
		return false
	}
	return true
}
