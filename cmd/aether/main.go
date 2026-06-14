// aether is the aethershell client — a transparent login shell wrapper.
//
// When invoked as an interactive login shell (TTY + no arguments), it
// connects to the per-user aetherd daemon and either creates a new
// persistent session, attaches to an existing one, or presents an
// interactive chooser when multiple sessions are available.
//
// Non-interactive invocations (no TTY, piped stdin, or command arguments)
// pass through to /bin/bash unchanged, so scp, rsync, sftp, and
// ssh host <cmd> all work normally.
//
// Usage:
//
//	aether                  # interactive: bring up the session chooser
//	aether --login              # smart login entrypoint (used by the profile.d hook)
//	aether ssh <host>           # reconnecting OpenSSH wrapper
//	aether ts <host>            # reconnecting Tailscale SSH wrapper
//	aether --menu               # session chooser; from within a session it switches in place
//	aether --new                # new session; from within a session, switches the terminal to it
//	aether --attach <name>      # attach a session by name; from within a session, switches in place
//	aether --list               # list sessions (for scripting)
//	aether --kill <name>        # kill a session
//	aether --takeover <name>    # force-attach a stale busy session
//	aether --upgrade-daemon     # hot-upgrade aetherd without killing sessions
//	aether --version            # print version
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Gwolfgit/aethershell/internal/client"
	"github.com/Gwolfgit/aethershell/internal/connect"
	"github.com/Gwolfgit/aethershell/internal/proto"
)

const version = "2.0.0"

func main() {
	// --- Explicit management commands (work from anywhere) ---

	// --version
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("aether", version)
		os.Exit(0)
	}

	// --list: dump session list as text (for scripting)
	if len(os.Args) > 1 && os.Args[1] == "--list" {
		cmdList()
		return
	}

	// ssh/connect: local reconnecting OpenSSH wrapper.
	if len(os.Args) > 1 && (os.Args[1] == "ssh" || os.Args[1] == "connect") {
		os.Exit(connect.SSHWithUsage(os.Args[2:], "usage: aether ssh [ssh options] host"))
		return
	}

	// ts/tailscale/tailscale-ssh: local reconnecting Tailscale SSH wrapper.
	if len(os.Args) > 1 && (os.Args[1] == "ts" || os.Args[1] == "tailscale" || os.Args[1] == "tailscale-ssh") {
		os.Exit(connect.TailscaleWithUsage(os.Args[2:], "usage: aether ts [tailscale ssh options] host"))
		return
	}

	// --kill <name>
	if len(os.Args) > 2 && os.Args[1] == "--kill" {
		cmdKill(os.Args[2])
		return
	}

	// --takeover <name>
	if len(os.Args) > 2 && os.Args[1] == "--takeover" {
		cmdTakeover(os.Args[2])
		return
	}

	// --upgrade-daemon
	if len(os.Args) > 1 && os.Args[1] == "--upgrade-daemon" {
		cmdUpgradeDaemon()
		return
	}

	// --new: create a fresh session (works from inside a session or login)
	if len(os.Args) > 1 && os.Args[1] == "--new" {
		cmdNew()
		return
	}

	// --attach <name>
	if len(os.Args) > 2 && os.Args[1] == "--attach" {
		cmdAttach(os.Args[2])
		return
	}

	// --menu: interactive chooser (works from inside a session or login)
	if len(os.Args) > 1 && os.Args[1] == "--menu" {
		cmdMenu()
		return
	}

	// --login: the smart login entrypoint used by the /etc/profile.d hook
	// (`exec aether --login`). Attaches/creates/chooses, or falls through to a
	// plain shell when aether shouldn't engage.
	if len(os.Args) > 1 && os.Args[1] == "--login" {
		loginFlow()
		return
	}

	// ------------------------------------------------
	// Non-interactive? Pass through to /bin/bash.
	// ------------------------------------------------
	if !isInteractive() {
		execBinary("/bin/bash", os.Args[1:]...)
		return
	}

	// A command was given (e.g. ssh host "ls -la") — pass through.
	if len(os.Args) > 1 {
		execBinary("/bin/bash", os.Args[1:]...)
		return
	}

	// Bare, interactive `aether` typed at a prompt → bring up the chooser.
	// (Implicit login interception is handled separately via `--login`.)
	cmdMenu()
}

// loginFlow is the smart entrypoint for remote logins: attach the single free
// session, create one when none exist, or show the chooser when there are
// several worth seeing. Falls through to a plain shell when aether shouldn't
// engage (local console, already nested, daemon trouble, or disabled).
func loginFlow() {
	if !isInteractive() {
		// No usable TTY on this connection. If aether was explicitly asked to
		// handle a remote login (the reconnect wrapper sets AETHERSHELL_FORCE_REMOTE)
		// the user wanted an interactive session, but their SSH client did not
		// allocate a PTY (e.g. missing `-t`/`-tt`). Persistent aether sessions are
		// PTY-backed and cannot work here — but DO NOT hand back a silent,
		// prompt-less /bin/bash that just looks like a hang. Explain why and give a
		// real interactive shell so the user can see the message and keep working.
		if os.Getenv("AETHERSHELL_FORCE_REMOTE") != "" {
			fmt.Fprintln(os.Stderr, "aether: no TTY was allocated for this connection, so persistent sessions are unavailable.")
			fmt.Fprintln(os.Stderr, "aether: reconnect with a TTY for full aether — e.g. `tailscale ssh user@host`, `ssh -tt user@host`, or update aether-connect.")
			execInteractiveShell()
			return
		}
		// scp/rsync/sftp and `ssh host <cmd>` legitimately have no TTY: pass through.
		execBinary("/bin/bash")
		return
	}
	// Already inside an aether/tmux/screen session? Don't nest.
	if os.Getenv("AETHER_SESSION") != "" || os.Getenv("TMUX") != "" || os.Getenv("STY") != "" {
		execBinary("/bin/bash")
		return
	}
	// Remote sessions only — a local console/serial login is never intercepted.
	if !isRemote() {
		execBinary("/bin/bash")
		return
	}
	// Emergency bypass: touch ~/.aethershell/disabled or set AETHER_DISABLE=1.
	if aetherDisabled() {
		execBinary("/bin/bash")
		return
	}

	c := ensureClient()
	clientID := cleanClientID(os.Getenv("AETHERSHELL_CLIENT_ID"))
	if clientID != "" {
		if err := c.AttachClientSession(clientID); err == nil {
			return
		}
	}

	sessions, err := c.ListSessions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "aether: %v\n", err)
		fmt.Fprintf(os.Stderr, "aether: starting a normal shell instead.\n")
		execBinary("/bin/bash")
		return
	}

	// free = detached, attachable, aether-managed sessions. External rows
	// (other live remote logins) are never auto-attached — they're read-only.
	free := freeSessions(sessions)

	switch {
	case len(free) == 0:
		// Nothing to attach — stay invisible and just hand over a fresh shell.
		if err := createSession(c, clientID); err != nil {
			fmt.Fprintf(os.Stderr, "aether: create session: %v\n", err)
			execBinary("/bin/bash")
		}

	case len(free) == 1 && len(sessions) == 1:
		// Exactly one session in the whole box-wide view — just attach to it.
		if err := attachSession(c, free[0], clientID); err != nil {
			fmt.Fprintf(os.Stderr, "aether: attach session: %v\n", err)
			execBinary("/bin/bash")
		}

	default:
		// Multiple sessions to consider (extra free sessions, or other live
		// logins worth seeing) → show the chooser.
		runChooserLoop(c, sessions, clientID)
	}
}

// --- helpers ---

func isInteractive() bool {
	stdin, _ := os.Stdin.Stat()
	stdout, _ := os.Stdout.Stat()
	return (stdin.Mode()&os.ModeCharDevice) != 0 && (stdout.Mode()&os.ModeCharDevice) != 0
}

// insideSession reports whether this aether invocation is running inside an
// aether-managed session (the daemon sets AETHER_SESSION in each session shell).
// When true, navigation switches the existing terminal in place instead of
// nesting a second client inside the current session.
func insideSession() bool {
	return os.Getenv("AETHER_SESSION") != ""
}

// currentTTY returns this process's controlling pseudo-terminal as "pts/N", used
// to tell the daemon which session to switch away from. Empty if it can't be
// determined (e.g. stdin is not a pts).
func currentTTY() string {
	for _, fd := range []string{"0", "1", "2"} {
		if l, err := os.Readlink("/proc/self/fd/" + fd); err == nil {
			if strings.HasPrefix(l, "/dev/pts/") {
				return strings.TrimPrefix(l, "/dev/")
			}
		}
	}
	return ""
}

// isRemote reports whether this login originated from a remote host. SSH (incl.
// Tailscale SSH and login(1) invoked with -h) exports SSH_CONNECTION/SSH_TTY;
// local console and serial logins do not.
func isRemote() bool {
	return os.Getenv("AETHERSHELL_FORCE_REMOTE") != "" ||
		os.Getenv("SSH_CONNECTION") != "" ||
		os.Getenv("SSH_TTY") != "" ||
		os.Getenv("SSH_CLIENT") != ""
}

func execBinary(bin string, args ...string) {
	argv := append([]string{bin}, args...)
	if err := syscall.Exec(bin, argv, os.Environ()); err != nil {
		log.Fatalf("exec %s: %v", bin, err)
	}
}

// execInteractiveShell replaces the process with an interactive bash. Unlike
// execBinary("/bin/bash"), the `-i` flag forces a prompt even when stdin is not
// a TTY (a pipe), so a connection without a PTY gets a usable prompt instead of
// a silent, prompt-less shell.
func execInteractiveShell() {
	if err := syscall.Exec("/bin/bash", []string{"/bin/bash", "-i"}, os.Environ()); err != nil {
		log.Fatalf("exec /bin/bash -i: %v", err)
	}
}

func socketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		p := filepath.Join(dir, "aethershell")
		return filepath.Join(p, "sock")
	}
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".aethershell")
	return filepath.Join(p, "sock")
}

func socketExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func aetherDisabled() bool {
	if os.Getenv("AETHER_DISABLE") != "" || os.Getenv("AETHER_BYPASS") != "" {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".aethershell", "disabled"))
	return err == nil
}

func startDaemon() {
	if startDaemonViaSystemd() {
		return
	}

	exe, err := os.Executable()
	var daemonPath string
	if err == nil {
		daemonPath = filepath.Join(filepath.Dir(exe), "aetherd")
		if _, err := os.Stat(daemonPath); err != nil {
			daemonPath = "aetherd"
		}
	} else {
		daemonPath = "aetherd"
	}

	cmd := exec.Command(daemonPath)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
	if err := cmd.Start(); err != nil {
		log.Printf("aether: failed to start daemon: %v", err)
	}
}

func startDaemonViaSystemd() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "systemctl", "--user", "start", "aetherd.service")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// ensureClient starts the daemon if needed and returns a connected client.
func ensureClient() *client.Client {
	sp := socketPath()
	if !socketExists(sp) {
		startDaemon()
		for i := 0; i < 30; i++ {
			time.Sleep(100 * time.Millisecond)
			if socketExists(sp) {
				break
			}
		}
	}
	return client.NewClient(sp)
}

// runChooserLoop runs the interactive chooser in a loop, handling attach/create/kill/etc.
func runChooserLoop(c *client.Client, sessions []proto.Session, clientID string) {
	message := ""
	for {
		choice := client.RunChooser(sessions, message)
		message = ""
		switch choice.Action {
		case "attach":
			if err := attachSession(c, choice.Name, clientID); err != nil {
				fmt.Fprintf(os.Stderr, "aether: attach: %v\n", err)
				continue
			}
			return
		case "takeover":
			if err := takeoverSession(c, choice.Name, clientID); err != nil {
				fmt.Fprintf(os.Stderr, "aether: take over: %v\n", err)
				continue
			}
			return
		case "new":
			if err := createSession(c, clientID); err != nil {
				fmt.Fprintf(os.Stderr, "aether: create: %v\n", err)
				continue
			}
			return
		case "kill":
			if err := c.KillSession(choice.Name); err != nil {
				message = "terminate failed: " + err.Error()
			} else {
				message = "terminated " + choice.Name
			}
			var err error
			sessions, err = c.ListSessions()
			if err != nil {
				message = "list failed: " + err.Error()
			}
			if len(sessions) == 0 {
				if err := createSession(c, clientID); err != nil {
					fmt.Fprintf(os.Stderr, "aether: create: %v\n", err)
					execBinary("/bin/bash")
				}
				return
			}
		case "restart":
			if err := c.RestartSession(choice.Name); err != nil {
				message = "restart failed: " + err.Error()
			} else {
				message = "restarted " + choice.Name
			}
			var err error
			sessions, err = c.ListSessions()
			if err != nil {
				message = "list failed: " + err.Error()
			}
		case "kill_all":
			if err := c.KillAllSessions(); err != nil {
				message = "terminate all failed: " + err.Error()
				continue
			}
			if err := createSession(c, clientID); err != nil {
				fmt.Fprintf(os.Stderr, "aether: create: %v\n", err)
				execBinary("/bin/bash")
			}
			return
		case "restart_all":
			if err := c.RestartAllSessions(); err != nil {
				message = "restart all failed: " + err.Error()
			} else {
				message = "restarted all sessions"
			}
			var err error
			sessions, err = c.ListSessions()
			if err != nil {
				message = "list failed: " + err.Error()
			}
		case "quit":
			os.Exit(0)
		}
	}
}

// --- Management commands (work from inside a session) ---

func cmdMenu() {
	c := ensureClient()
	sessions, err := c.ListSessions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "aether: %v\n", err)
		os.Exit(1)
	}
	if insideSession() && c.SupportsSwitch() {
		runChooserLoopInSession(c, sessions, currentTTY())
		return
	}
	runChooserLoop(c, sessions, cleanClientID(os.Getenv("AETHERSHELL_CLIENT_ID")))
}

func cmdNew() {
	c := ensureClient()
	if insideSession() && c.SupportsSwitch() {
		if err := c.SwitchInPlace(currentTTY(), "", false, true, client.CurrentGeometry()); err != nil {
			fmt.Fprintf(os.Stderr, "aether: new: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := createSession(c, cleanClientID(os.Getenv("AETHERSHELL_CLIENT_ID"))); err != nil {
		fmt.Fprintf(os.Stderr, "aether: create: %v\n", err)
		os.Exit(1)
	}
}

func cmdAttach(name string) {
	c := ensureClient()
	if insideSession() && c.SupportsSwitch() {
		if err := c.SwitchInPlace(currentTTY(), name, false, false, client.CurrentGeometry()); err != nil {
			fmt.Fprintf(os.Stderr, "aether: switch: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := attachSession(c, name, cleanClientID(os.Getenv("AETHERSHELL_CLIENT_ID"))); err != nil {
		fmt.Fprintf(os.Stderr, "aether: attach: %v\n", err)
		os.Exit(1)
	}
}

func cmdTakeover(name string) {
	c := ensureClient()
	if insideSession() && c.SupportsSwitch() {
		if err := c.SwitchInPlace(currentTTY(), name, true, false, client.CurrentGeometry()); err != nil {
			fmt.Fprintf(os.Stderr, "aether: switch: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := takeoverSession(c, name, cleanClientID(os.Getenv("AETHERSHELL_CLIENT_ID"))); err != nil {
		fmt.Fprintf(os.Stderr, "aether: take over: %v\n", err)
		os.Exit(1)
	}
}

// runChooserLoopInSession is the chooser when invoked from inside a session.
// Selecting another session switches the existing terminal to it in place (via
// the daemon) rather than nesting a new client; management actions act directly.
func runChooserLoopInSession(c *client.Client, sessions []proto.Session, tty string) {
	if tty == "" {
		fmt.Fprintln(os.Stderr, "aether: cannot determine current tty; not switching")
		return
	}
	message := ""
	refresh := func() {
		if s, err := c.ListSessions(); err == nil {
			sessions = s
		}
	}
	for {
		choice := client.RunChooser(sessions, message)
		message = ""
		switch choice.Action {
		case "attach":
			if err := c.SwitchInPlace(tty, choice.Name, false, false, client.CurrentGeometry()); err != nil {
				message = "switch failed: " + err.Error()
				continue
			}
			return
		case "takeover":
			if err := c.SwitchInPlace(tty, choice.Name, true, false, client.CurrentGeometry()); err != nil {
				message = "switch failed: " + err.Error()
				continue
			}
			return
		case "new":
			if err := c.SwitchInPlace(tty, "", false, true, client.CurrentGeometry()); err != nil {
				message = "switch failed: " + err.Error()
				continue
			}
			return
		case "kill":
			if err := c.KillSession(choice.Name); err != nil {
				message = "terminate failed: " + err.Error()
			} else {
				message = "terminated " + choice.Name
			}
			refresh()
		case "restart":
			if err := c.RestartSession(choice.Name); err != nil {
				message = "restart failed: " + err.Error()
			} else {
				message = "restarted " + choice.Name
			}
			refresh()
		case "kill_all":
			if err := c.KillAllSessions(); err != nil {
				message = "terminate all failed: " + err.Error()
			}
			refresh()
		case "restart_all":
			if err := c.RestartAllSessions(); err != nil {
				message = "restart all failed: " + err.Error()
			} else {
				message = "restarted all sessions"
			}
			refresh()
		case "quit":
			return
		}
	}
}

func cmdList() {
	c := client.NewClient(socketPath())
	sessions, err := c.ListSessions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "aether: %v\n", err)
		os.Exit(1)
	}
	for _, s := range sessions {
		status := "free"
		switch {
		case s.External:
			status = "external"
		case s.Attached:
			status = "busy"
		}
		label := s.Agent.Title
		if label == "" || label == "shell" {
			label = "shell"
		}
		host := ""
		if s.RemoteHost != "" {
			host = "  <" + proto.SanitizeTerminal(s.RemoteHost) + ">"
		}
		// Session metadata is derived from untrusted /proc + utmp data; strip
		// control bytes so a crafted dir/cmdline name cannot inject terminal
		// escape sequences into the operator's terminal.
		fmt.Printf("%-40s %-9s %s in %s%s\n",
			proto.SanitizeTerminal(s.Name), status,
			proto.SanitizeTerminal(label), proto.SanitizeTerminal(s.Agent.WorkDir), host)
	}
}

func cmdKill(name string) {
	c := client.NewClient(socketPath())
	if err := c.KillSession(name); err != nil {
		fmt.Fprintf(os.Stderr, "aether: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("killed session %q\n", name)
}

func cmdUpgradeDaemon() {
	c := client.NewClient(socketPath())
	if err := c.UpgradeDaemon(); err != nil {
		fmt.Fprintf(os.Stderr, "aether: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("aetherd hot-upgraded")
}

func freeSessions(sessions []proto.Session) []string {
	var free []string
	for _, s := range sessions {
		if !s.External && !s.Attached {
			free = append(free, s.Name)
		}
	}
	return free
}

func createSession(c *client.Client, clientID string) error {
	if clientID != "" {
		return c.CreateSessionForClient(clientID)
	}
	return c.CreateSession()
}

func attachSession(c *client.Client, name, clientID string) error {
	if clientID != "" {
		return c.AttachSessionForClient(name, clientID)
	}
	return c.AttachSession(name)
}

func takeoverSession(c *client.Client, name, clientID string) error {
	if clientID != "" {
		return c.AttachSessionTakeoverForClient(name, clientID)
	}
	return c.AttachSessionTakeover(name)
}

func cleanClientID(clientID string) string {
	clientID = strings.TrimSpace(clientID)
	if len(clientID) > 128 {
		return ""
	}
	return clientID
}
