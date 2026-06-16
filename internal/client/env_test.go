package client

import "testing"

func TestConnEnvAllowlistAndLCPrefix(t *testing.T) {
	// Allowlisted terminal/connection vars are forwarded.
	t.Setenv("TERM", "screen-256color")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_CTYPE", "en_US.UTF-8")
	t.Setenv("SSH_CONNECTION", "a b c d")
	// Empty values are not forwarded (must not clobber the account's value).
	t.Setenv("COLORTERM", "")
	// Unrelated vars are not forwarded.
	t.Setenv("AETHERSHELL_CLIENT_ID", "should-not-forward")
	t.Setenv("PATH", "/should/not/forward")

	env := ConnEnv()

	if env["TERM"] != "screen-256color" {
		t.Errorf("TERM = %q, want screen-256color", env["TERM"])
	}
	if env["LANG"] != "en_US.UTF-8" {
		t.Errorf("LANG = %q, want en_US.UTF-8", env["LANG"])
	}
	if env["LC_CTYPE"] != "en_US.UTF-8" {
		t.Errorf("LC_CTYPE = %q, want en_US.UTF-8 (LC_* must be forwarded)", env["LC_CTYPE"])
	}
	if env["SSH_CONNECTION"] != "a b c d" {
		t.Errorf("SSH_CONNECTION = %q, want forwarded", env["SSH_CONNECTION"])
	}
	if _, ok := env["COLORTERM"]; ok {
		t.Error("empty COLORTERM should not be forwarded")
	}
	if _, ok := env["AETHERSHELL_CLIENT_ID"]; ok {
		t.Error("AETHERSHELL_CLIENT_ID must not be forwarded")
	}
	if _, ok := env["PATH"]; ok {
		t.Error("PATH must not be forwarded (account provides it)")
	}
}
