package connect

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// SSH runs OpenSSH in a reconnect loop and asks the remote side to enter
// aether's login flow with a stable local client identity.
func SSH(args []string) int {
	return SSHWithUsage(args, "usage: aether-connect ssh [ssh options] host")
}

func SSHWithUsage(args []string, usage string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		fmt.Fprintf(os.Stderr, "aether: ssh not found: %v\n", err)
		return 1
	}
	clientID, err := clientID()
	if err != nil {
		fmt.Fprintf(os.Stderr, "aether: generate client id: %v\n", err)
		return 1
	}

	base := []string{
		"-tt",
		"-o", "ServerAliveInterval=10",
		"-o", "ServerAliveCountMax=12",
		"-o", "TCPKeepAlive=no",
	}
	base = append(base, args...)
	base = append(base, remoteLoginCommand(clientID))

	return runReconnectingTransport("ssh", base, func(code int) bool {
		return code == 255
	})
}

// Tailscale runs `tailscale ssh` in a reconnect loop. This path does not
// require sshd on the remote host; it uses Tailscale SSH via the local
// tailscale CLI.
func Tailscale(args []string) int {
	return TailscaleWithUsage(args, "usage: aether-connect ts [tailscale ssh options] host")
}

func TailscaleWithUsage(args []string, usage string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	if _, err := exec.LookPath("tailscale"); err != nil {
		fmt.Fprintf(os.Stderr, "aether: tailscale not found: %v\n", err)
		return 1
	}
	clientID, err := clientID()
	if err != nil {
		fmt.Fprintf(os.Stderr, "aether: generate client id: %v\n", err)
		return 1
	}

	base := []string{"ssh"}
	base = append(base, args...)
	base = append(base, remoteLoginCommand(clientID))

	return runReconnectingTransport("tailscale", base, func(code int) bool {
		// tailscale ssh does not use OpenSSH's conventional 255 transport
		// status consistently. For this wrapper, any non-zero transport exit is
		// treated as reconnectable; the user can stop the loop with Ctrl+C.
		return code != 0 && code != 130
	})
}

func clientID() (string, error) {
	clientID := cleanClientID(os.Getenv("AETHERSHELL_CLIENT_ID"))
	if clientID != "" {
		return clientID, nil
	}
	return newClientID()
}

func cleanClientID(clientID string) string {
	clientID = strings.TrimSpace(clientID)
	if len(clientID) > 128 {
		return ""
	}
	return clientID
}

func remoteLoginCommand(clientID string) string {
	return "AETHERSHELL_CLIENT_ID=" + shellQuote(clientID) + " AETHERSHELL_FORCE_REMOTE=1 exec aether --login"
}

func runReconnectingTransport(bin string, args []string, shouldReconnect func(int) bool) int {
	backoff := time.Second
	reported := false
	for {
		cmd := exec.Command(bin, args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = sshNoiseFilter{}

		err := cmd.Run()
		if err == nil {
			return 0
		}
		code := exitCode(err)
		if !shouldReconnect(code) {
			fmt.Fprintf(os.Stderr, "aether: %s exited: %v\n", bin, err)
			return code
		}
		if !reported {
			fmt.Fprintln(os.Stderr, "\r\naether: reconnecting...")
			reported = true
		}
		time.Sleep(backoff)
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

func newClientID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

type sshNoiseFilter struct{}

func (sshNoiseFilter) Write(p []byte) (int, error) {
	text := string(p)
	if strings.Contains(text, "client_loop: send disconnect") ||
		strings.Contains(text, "Broken pipe") {
		return len(p), nil
	}
	n, err := os.Stderr.Write(p)
	if err != nil {
		return n, err
	}
	if n < len(p) {
		return n, syscall.EIO
	}
	return len(p), nil
}
