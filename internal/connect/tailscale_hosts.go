package connect

import (
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// sshFlagsWithValue are the OpenSSH option letters that consume the following
// argument as their value (from ssh(1)). We mirror ssh's own parsing so we
// don't mistake a flag value for the destination host. `tailscale ssh` accepts
// a compatible subset, so the same table is safe for both transports.
const sshFlagsWithValue = "bcDEeFIiJLlmOopQRSWw"

// tailscaleNode is the subset of `tailscale status --json` we care about.
type tailscaleNode struct {
	DNSName      string   `json:"DNSName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
}

type tailscaleStatus struct {
	Self *tailscaleNode            `json:"Self"`
	Peer map[string]*tailscaleNode `json:"Peer"`
}

// tailscaleHosts returns a map of {short_hostname: tailscale_ip} built from
// `tailscale status --json`. It returns an empty map (never nil-panics) if
// tailscale is unavailable, not running, or produces no usable data. This is
// the gate for "only when the user is using Tailscale": no tailscale, no
// resolution, and the destination is left untouched.
func tailscaleHosts() map[string]string {
	hosts := map[string]string{}

	if _, err := exec.LookPath("tailscale"); err != nil {
		return hosts
	}

	cmd := exec.Command("tailscale", "status", "--json")
	out, err := cmd.Output()
	if err != nil {
		return hosts
	}

	var status tailscaleStatus
	if err := json.Unmarshal(out, &status); err != nil {
		return hosts
	}

	add := func(node *tailscaleNode) {
		if node == nil || len(node.TailscaleIPs) == 0 {
			return
		}
		dns := strings.TrimSuffix(node.DNSName, ".")
		if dns == "" {
			return
		}
		short := strings.SplitN(dns, ".", 2)[0]
		if short != "" {
			hosts[short] = node.TailscaleIPs[0]
		}
	}

	for _, peer := range status.Peer {
		add(peer)
	}
	// Self last so it cannot be shadowed by a peer of the same short name.
	add(status.Self)

	return hosts
}

// withResolveTimeout is a thin seam so tailscaleHosts never blocks the connect
// loop for long if the tailscale CLI hangs.
func resolveTailscaleHosts() map[string]string {
	type result struct{ hosts map[string]string }
	ch := make(chan result, 1)
	go func() { ch <- result{tailscaleHosts()} }()
	select {
	case r := <-ch:
		return r.hosts
	case <-time.After(5 * time.Second):
		return map[string]string{}
	}
}

// findDestIndex returns the index of the [user@]host argument in an ssh-style
// argument list, or -1 if there isn't one. It mirrors ssh's argument parsing so
// flag values are not mistaken for the destination.
func findDestIndex(args []string) int {
	i := 0
	for i < len(args) {
		arg := args[i]

		if arg == "--" {
			if i+1 < len(args) {
				return i + 1
			}
			return -1
		}

		if strings.HasPrefix(arg, "-") && len(arg) > 1 {
			flag := arg[1]
			if strings.IndexByte(sshFlagsWithValue, flag) >= 0 && len(arg) == 2 {
				// "-o value": skip both the flag and its value.
				i += 2
			} else {
				// "-ovalue" or a boolean flag: skip just this token.
				i++
			}
			continue
		}

		return i
	}
	return -1
}

// resolveTailscaleDest rewrites a short Tailscale hostname in the destination
// argument to its Tailscale IP, in place. It is a no-op when Tailscale is not
// in use or the host is not a known Tailscale peer, so OpenSSH-to-public-host
// behavior is preserved. Returns the resolved host (or the original) for
// logging, and whether a substitution happened.
func resolveTailscaleDest(args []string) (string, bool) {
	idx := findDestIndex(args)
	if idx < 0 {
		return "", false
	}

	dest := args[idx]
	userPrefix, host := "", dest
	if at := strings.LastIndex(dest, "@"); at >= 0 {
		userPrefix, host = dest[:at+1], dest[at+1:]
	}

	hosts := resolveTailscaleHosts()
	ip, ok := hosts[host]
	if !ok {
		return host, false
	}

	args[idx] = userPrefix + ip
	return host, true
}
