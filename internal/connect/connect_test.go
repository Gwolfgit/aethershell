package connect

import (
	"reflect"
	"testing"
)

func TestSSHTransportArgsForceTTY(t *testing.T) {
	got := sshTransportArgs([]string{"gwolf@cosmo"}, "client-1")
	want := []string{
		"-tt",
		"-o", "ServerAliveInterval=10",
		"-o", "ServerAliveCountMax=12",
		"-o", "TCPKeepAlive=no",
		"gwolf@cosmo",
		"AETHERSHELL_CLIENT_ID='client-1' AETHERSHELL_FORCE_REMOTE=1 exec aether --login",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sshTransportArgs() = %#v, want %#v", got, want)
	}
}

func TestTailscaleTransportArgsForceTTY(t *testing.T) {
	got := tailscaleTransportArgs([]string{"gwolf@cosmo"}, "client-1")
	want := []string{
		"ssh",
		"gwolf@cosmo",
		"if command -v script >/dev/null 2>&1; then exec script -q /dev/null -c 'AETHERSHELL_CLIENT_ID='\\''client-1'\\'' AETHERSHELL_FORCE_REMOTE=1 exec aether --login'; else echo 'aether: tailscale ssh cannot allocate a TTY for remote commands and script(1) is missing; use aether-connect ssh host' >&2; exit 127; fi",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tailscaleTransportArgs() = %#v, want %#v", got, want)
	}
}
