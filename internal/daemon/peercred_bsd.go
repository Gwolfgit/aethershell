//go:build darwin || freebsd
// +build darwin freebsd

package daemon

import (
	"errors"
	"log"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

var errNoPeerCred = errors.New("connection has no peer credentials")

func peerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errNoPeerCred
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var xcred *unix.Xucred
	var sockErr error
	if cerr := raw.Control(func(fd uintptr) {
		xcred, sockErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); cerr != nil {
		return 0, cerr
	}
	if sockErr != nil {
		return 0, sockErr
	}
	if xcred == nil {
		return 0, errNoPeerCred
	}
	return xcred.Uid, nil
}

func (s *Server) authorizePeer(conn net.Conn) bool {
	uid, err := peerUID(conn)
	if err != nil {
		if errors.Is(err, errNoPeerCred) {
			return true
		}
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
