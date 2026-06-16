package daemon

import (
	"strings"
	"testing"

	"github.com/Gwolfgit/aethershell/internal/proto"
)

// TestPerClientSessionCeiling verifies that a single client cannot create
// sessions without bound: once it owns maxSessionsPerClient live sessions, a
// further create is refused rather than minting another shell.
func TestPerClientSessionCeiling(t *testing.T) {
	defer func(prev int) { maxSessionsPerClient = prev }(maxSessionsPerClient)
	maxSessionsPerClient = 2

	srv := NewServer("")
	t.Cleanup(func() {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		for _, sess := range srv.sessions {
			sess.Kill()
		}
	})

	var conns []interface{ Close() error }
	for i := 0; i < maxSessionsPerClient; i++ {
		conn, _ := openTestAttach(t, srv, proto.Request{Type: "create", ClientID: "runaway", Rows: 24, Cols: 80})
		conns = append(conns, conn)
	}
	t.Cleanup(func() {
		for _, c := range conns {
			c.Close()
		}
	})

	// The next create from the same client must be rejected.
	expectTestAttachError(t, srv, proto.Request{Type: "create", ClientID: "runaway", Rows: 24, Cols: 80})

	// A different client is unaffected by another client's ceiling.
	conn, _ := openTestAttach(t, srv, proto.Request{Type: "create", ClientID: "other", Rows: 24, Cols: 80})
	conn.Close()
}

// TestBuildSessionEnvForwardsConnectionEnv verifies the session shell inherits
// the forwarded connection environment (terminal type, locale) rather than a
// hardcoded value, while aether's own markers still win.
func TestBuildSessionEnvForwardsConnectionEnv(t *testing.T) {
	connEnv := map[string]string{
		"TERM":           "xterm-kitty",
		"LANG":           "en_US.UTF-8",
		"LC_TIME":        "de_DE.UTF-8",
		"SSH_CONNECTION": "100.1.2.3 4321 100.4.5.6 22",
	}
	env := toMap(buildSessionEnv("shell-1", "client-x", Geometry{Rows: 40, Cols: 120}, connEnv))

	if env["TERM"] != "xterm-kitty" {
		t.Errorf("TERM = %q, want forwarded xterm-kitty (not a hardcoded default)", env["TERM"])
	}
	if env["LANG"] != "en_US.UTF-8" {
		t.Errorf("LANG = %q, want forwarded en_US.UTF-8", env["LANG"])
	}
	if env["LC_TIME"] != "de_DE.UTF-8" {
		t.Errorf("LC_TIME = %q, want forwarded de_DE.UTF-8", env["LC_TIME"])
	}
	if env["SSH_CONNECTION"] == "" {
		t.Error("SSH_CONNECTION was dropped; should be forwarded")
	}
	if env["AETHER_SESSION"] != "shell-1" {
		t.Errorf("AETHER_SESSION = %q, want shell-1", env["AETHER_SESSION"])
	}
	if env["AETHERSHELL_CLIENT_ID"] != "client-x" {
		t.Errorf("AETHERSHELL_CLIENT_ID = %q, want client-x", env["AETHERSHELL_CLIENT_ID"])
	}
}

// TestBuildSessionEnvTermFallback verifies TERM defaults only when the
// connection supplied none.
func TestBuildSessionEnvTermFallback(t *testing.T) {
	env := toMap(buildSessionEnv("s", "", Geometry{Rows: 24, Cols: 80}, nil))
	if env["TERM"] != "xterm-256color" {
		t.Errorf("TERM fallback = %q, want xterm-256color", env["TERM"])
	}
}

func toMap(kvs []string) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			m[kv[:eq]] = kv[eq+1:]
		}
	}
	return m
}
