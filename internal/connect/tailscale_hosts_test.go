package connect

import "testing"

func TestFindDestIndex(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"bare host", []string{"core"}, 0},
		{"user@host", []string{"gwolf@cosmo"}, 0},
		{"flag with value then host", []string{"-p", "2222", "core"}, 2},
		{"attached flag value then host", []string{"-p2222", "core"}, 1},
		{"boolean flag then host", []string{"-v", "core"}, 1},
		{"identity flag then host", []string{"-i", "key", "user@core"}, 2},
		{"double dash", []string{"--", "core"}, 1},
		{"no host", []string{"-v"}, -1},
		{"flag consumes last arg", []string{"-p"}, -1},
		{"host before remote command", []string{"core", "uptime"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := findDestIndex(tc.args); got != tc.want {
				t.Fatalf("findDestIndex(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}

func TestResolveTailscaleDestNoMatch(t *testing.T) {
	// With no matching peer (and likely no tailscale in test env), the
	// destination must be left untouched and report no substitution.
	args := []string{"-p", "22", "definitely-not-a-tailscale-peer.invalid"}
	host, ok := resolveTailscaleDest(args)
	if ok {
		t.Fatalf("unexpected substitution to %q for unknown host", args[2])
	}
	if host != "definitely-not-a-tailscale-peer.invalid" {
		t.Fatalf("host = %q, want original", host)
	}
	if args[2] != "definitely-not-a-tailscale-peer.invalid" {
		t.Fatalf("args mutated: %v", args)
	}
}

func TestResolveTailscaleDestNoHost(t *testing.T) {
	args := []string{"-v"}
	if _, ok := resolveTailscaleDest(args); ok {
		t.Fatal("expected no substitution when there is no destination")
	}
}
