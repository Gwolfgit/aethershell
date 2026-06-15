// aether-connect is the local-only AetherShell transport wrapper.
//
// It does not start or talk to a local aetherd. Install it on a workstation or
// laptop to reconnect to remote AetherShell hosts over Tailscale SSH or
// OpenSSH. The remote host must have `aether` installed.
package main

import (
	"fmt"
	"os"

	"github.com/Gwolfgit/aethershell/internal/connect"
)

const version = "2.0.1"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-v":
			fmt.Println("aether-connect", version)
			return
		case "--help", "-h", "help":
			usage()
			return
		case "ssh":
			os.Exit(connect.SSH(os.Args[2:]))
		case "ts", "tailscale", "tailscale-ssh":
			os.Exit(connect.Tailscale(os.Args[2:]))
		}
	}

	// The connector is primarily for machines that use Tailscale SSH locally.
	// Keep the common path short: `aether-connect host`.
	os.Exit(connect.Tailscale(os.Args[1:]))
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  aether-connect host                         # Tailscale SSH
  aether-connect ts [tailscale ssh opts] host # Tailscale SSH
  aether-connect ssh [ssh opts] host          # OpenSSH

Short Tailscale hostnames are resolved to their Tailscale IP automatically
(e.g. aether-connect ssh cosmo), but only when Tailscale is in use.

The local machine only needs this connector binary plus tailscale/ssh.
The remote host must have aether installed.`)
}
