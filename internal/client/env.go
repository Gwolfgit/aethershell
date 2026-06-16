package client

import (
	"os"
	"strings"
)

// connEnvNames are the environment variables that describe the terminal and
// connection the user arrived on. Because `aether --login` runs inside the SSH
// session, these already hold whatever the SSH layer negotiated for this login.
// Forwarding them to a freshly created session shell keeps the shell (and
// anything it launches) consistent with the real connection — the correct
// terminal type, locale, and connection identity — instead of the daemon's
// stale startup environment.
//
// This mirrors what OpenSSH itself propagates: TERM (via the PTY request) plus
// the default `SendEnv LANG LC_*`, and the SSH_* vars the server sets on login.
var connEnvNames = []string{
	"TERM",
	"COLORTERM",
	"TERM_PROGRAM",
	"TERM_PROGRAM_VERSION",
	"LANG",
	"LANGUAGE",
	"SSH_CONNECTION",
	"SSH_CLIENT",
	"SSH_TTY",
}

// ConnEnv returns the forwardable connection environment for the current login:
// the allowlisted terminal/connection vars plus every LC_* locale variable that
// is set. Empty values are omitted so they never clobber a value the shell would
// otherwise inherit from the user's account.
func ConnEnv() map[string]string {
	env := make(map[string]string)
	for _, name := range connEnvNames {
		if v, ok := os.LookupEnv(name); ok && v != "" {
			env[name] = v
		}
	}
	// All LC_* locale categories (LC_ALL, LC_CTYPE, LC_MESSAGES, ...).
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		k, v := kv[:eq], kv[eq+1:]
		if v != "" && strings.HasPrefix(k, "LC_") {
			env[k] = v
		}
	}
	return env
}
