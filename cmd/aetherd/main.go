// aetherd is the aethershell daemon.
// It manages persistent PTY sessions and serves client connections
// over a per-user Unix domain socket.
//
// Usage: aetherd
//
// The socket path is ~/.aethershell/sock — a single stable location so the
// daemon and every client agree regardless of how the user connected.
package main

import (
	"log"
	"os"

	"github.com/Gwolfgit/aethershell/internal/daemon"
	"github.com/Gwolfgit/aethershell/internal/sockpath"
)

func main() {
	log.SetPrefix("[aetherd] ")
	log.SetFlags(log.Ltime)

	socketPath := socketPath()
	if len(os.Args) == 3 && os.Args[1] == "--restore" {
		log.Printf("restoring daemon (socket: %s)", socketPath)
		srv, listener, err := daemon.Restore(socketPath, os.Args[2])
		if err != nil {
			log.Fatalf("restore: %v", err)
		}
		if err := srv.Serve(listener); err != nil {
			log.Fatalf("fatal: %v", err)
		}
		return
	}

	log.Printf("starting daemon (socket: %s)", socketPath)

	srv := daemon.NewServer(socketPath)
	if err := srv.Start(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func socketPath() string {
	return sockpath.Socket()
}
