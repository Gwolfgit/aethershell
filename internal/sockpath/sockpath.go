// Package sockpath resolves the per-user aethershell runtime paths.
//
// It deliberately does NOT key off XDG_RUNTIME_DIR. That variable is set
// inconsistently across login types — notably it is absent for non-PTY
// Tailscale SSH command sessions — which previously made the daemon and its
// clients disagree on the socket path and spin up a second, parallel daemon on
// the fallback path. XDG_RUNTIME_DIR (/run/user/<uid>) is also wiped by logind
// on full logout, which is wrong for a persistence daemon whose whole job is to
// outlive individual logins. A single stable path under the user's home
// directory guarantees exactly one daemon regardless of how the user connects.
package sockpath

import (
	"os"
	"path/filepath"
)

// Dir returns the aethershell runtime directory (~/.aethershell), creating it
// with 0700 permissions. If the home directory cannot be determined (which
// should not happen in practice) it falls back to ".aethershell" in the current
// directory so callers still get a usable, consistent path.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	p := filepath.Join(home, ".aethershell")
	os.MkdirAll(p, 0700)
	return p
}

// Socket returns the daemon's Unix domain socket path.
func Socket() string { return filepath.Join(Dir(), "sock") }

// Geometry returns the saved terminal-geometry file path.
func Geometry() string { return filepath.Join(Dir(), "geometry.json") }
