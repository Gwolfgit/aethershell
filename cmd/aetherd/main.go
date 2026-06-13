// aetherd is the aethershell daemon.
// It manages persistent PTY sessions and serves client connections
// over a per-user Unix domain socket.
//
// Usage: aetherd
//
// The socket path is $XDG_RUNTIME_DIR/aethershell/sock,
// falling back to ~/.aethershell/sock.
package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/Gwolfgit/aethershell/internal/daemon"
)

func main() {
	log.SetPrefix("[aetherd] ")
	log.SetFlags(log.Ltime)

	socketPath := socketPath()
	log.Printf("starting daemon (socket: %s)", socketPath)

	srv := daemon.NewServer(socketPath)
	if err := srv.Start(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func socketPath() string {
	// Prefer XDG_RUNTIME_DIR (per-user runtime dir, typically /run/user/<uid>)
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		p := filepath.Join(dir, "aethershell")
		os.MkdirAll(p, 0700)
		return filepath.Join(p, "sock")
	}
	// Fall back to home directory
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("cannot determine home directory: %v", err)
	}
	p := filepath.Join(home, ".aethershell")
	os.MkdirAll(p, 0700)
	return filepath.Join(p, "sock")
}
