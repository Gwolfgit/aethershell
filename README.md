# aethershell

> Invisible, persistent shell. You SSH in, you get a plain prompt — and your
> shell and everything in it survive the disconnect. No status bar, no borders,
> no prefix key. Just a shell that refuses to die.

`aethershell` is a Go-native login shell wrapper: every interactive SSH session
is backed by a per-user **aetherd** daemon managing persistent PTY sessions,
with no tmux or screen dependency. It works with OpenSSH, Tailscale SSH,
Dropbear — anything that invokes a login shell with a TTY — and it is especially
smooth over Tailscale. Close your laptop at home, reopen it at a nearby cafe,
and reconnect to the same shell with your coding agent, editor, and long-running
jobs still exactly where you left them.

---

## Why

SSH disconnect kills your shell and every foreground process with it. The usual
fix is "remember to run `tmux` / `screen` yourself," which nobody does until
they've lost a 40-minute build. `aethershell` makes persistence the *default*
and *invisible*: the persistence layer is completely hidden, so it just looks
and feels like a normal shell.

No tmux. No screen. Just a lightweight Go daemon managing PTYs.

## Features

- **Pure Go, zero dependencies** — a small daemon (`aetherd`) manages PTY
  sessions directly; the client (`aether`) connects over a Unix socket and
  pipes I/O to your terminal. No tmux, no screen, no external runtime.
- **Survives disconnects** — shells and running processes persist across drops.
- **Reconnect wrappers** — `aether ssh host` runs OpenSSH in a reconnect loop;
  `aether-connect host` or `aether ts host` runs Tailscale SSH through the
  `tailscale` CLI and does not require `sshd` on the remote host. Both pass a
  stable client ID to the remote side and reattach that terminal to its original
  session after a transport drop.
- **Remote-only** — only *remote* (SSH/Tailscale SSH) interactive logins are
  intercepted. A local **console or serial login is never touched** — it falls
  straight through to a normal shell. The login shell stays `/bin/bash`; a
  `/etc/profile.d/aether.sh` hook hands off only when `SSH_CONNECTION` is set.
- **Smart attach** — on login:
  - a known client ID → reattach to that client's previous session
  - no free session → create one
  - one free session → attach to it automatically
  - several free sessions → an interactive chooser (see below)
  - a session already in use by another live connection is **never**
    mirrored — you get your own session instead.
- **Box-wide session view** — the chooser shows a *complete* picture: your
  aether-managed sessions (attachable) **plus** every other live remote login on
  the box (other SSH sessions, editors, agents), discovered via `utmp` + `/proc`.
  External logins are shown **read-only** — labelled with their tty, remote host,
  and what's running — since aether holds no PTY master to attach to them.
- **Nice menu** — a colored, columned full-screen chooser: status-tinted rows
  (green free / yellow in-use / dim external), a selection highlight, and
  per-row tty, agent/command, working dir, age, and remote host.
  In-use managed sessions can be explicitly taken over when a stale transport
  still appears attached.
- **Agent detection** — identifies what's running in each session (coding agents,
  editors, CLIs, etc.) by scanning the process tree — works for
  both managed and external sessions, even when the agent is a grandchild of the
  login shell (`login → bash → agent`).
- **Dynamic tab and session names** — attached terminals that support OSC title
  sequences (including GNOME Terminal/Terminator) are titled by what matters:
  coding agents use `Project/.agent` such as `Aethershell/.codex`, while idle
  shells use `Host:/.shell` such as `Cosmo:/.bash`. Managed session names follow
  the same format.
- **Terminal geometry capture** — captures the SSH client's terminal size (and
  pixel dimensions/orientation when reported), caches it, applies it to the PTY,
  and exports `AETHER_GEOMETRY`/`COLUMNS`/`LINES` into the shell so an onward
  `ssh` hop to another aether box inherits the right size.
- **Per-user isolation** — each user gets a private daemon instance via a Unix
  socket. Isolation is enforced in depth: a `0700` runtime dir, a `0600` socket,
  **and** an `SO_PEERCRED` peer-uid check in the daemon, so sessions stay private
  even if the file permissions are ever loosened. See [SECURITY.md](SECURITY.md).
- **Doesn't break tooling** — `scp`, `rsync`, `sftp`, and `ssh host <cmd>` all
  bypass the wrapper and run a normal shell. No nesting when already inside
  tmux/screen.
- **Terminal-friendly** — raw PTY pass-through works cleanly with `vim`, `nano`,
  `htop`, `less`, and all TUI applications.
- **Excellent with Tailscale SSH** — roaming between networks feels natural:
  reconnect and keep working in the same shell without rebuilding context.

## Architecture

```
  aether ssh/connect  →  OpenSSH transport      →  aether --login  →  Unix socket  →  aetherd
  aether ts           →  Tailscale SSH transport ┘
     │              │                                      │
     │              │  framed I/O + client ID affinity     │
     │              └──────────────────────────────────────┘
     │                                                    │
     ▼                                                    ▼
  stdin/stdout ←→ PTY ←→ /bin/bash (persistent)
```

- **`aether ssh` / `aether connect`** — local SSH wrapper. It generates one
  stable `AETHERSHELL_CLIENT_ID` per wrapper process, runs SSH with keepalive
  settings, filters expected broken-pipe noise, and reconnects with the same
  client ID after transport failure.
- **`aether ts` / `aether tailscale` / `aether tailscale-ssh`** — local
  Tailscale SSH wrapper. It invokes `tailscale ssh`, so the remote machine only
  needs Tailscale SSH enabled; a normal OpenSSH server does not need to be
  running.
- **`aether-connect`** — local-only connector binary for laptops/workstations.
  It does not start or talk to a local `aetherd`; it only runs the reconnecting
  transport and asks the remote host to run `aether --login`. By default,
  `aether-connect host` uses Tailscale SSH. Use `aether-connect ssh host` for
  OpenSSH.
- **`aether`** — invoked for remote logins by the `/etc/profile.d/aether.sh`
  hook (login shell stays `/bin/bash`) or explicitly by the reconnect wrapper.
  Detects interactive vs non-interactive and remote vs local use, connects to
  the daemon, and streams PTY I/O over a private Unix socket. A local console
  login is never intercepted.
- **`aetherd`** — per-user daemon that spawns and manages PTY-backed bash
  sessions. Listens on `$XDG_RUNTIME_DIR/aethershell/sock` (or
  `~/.aethershell/sock`). Normally started as a `systemd --user` service by
  the client on first connect, with a direct daemon-spawn fallback for systems
  without user systemd.
- **PTY sessions** — each session is a running `/bin/bash` in a pseudoterminal.
  The PTY master fd stays open in the daemon even after the client disconnects,
  so the shell and all its children keep running.

## The session chooser

Run `aether --menu` any time, or reconnect when more than one managed session is
free, to get the full-screen chooser. It shows two kinds of rows:

- **`●` managed** — aether-owned sessions you can attach/kill/restart.
- **`○` external** — other live remote logins on the box (other SSH sessions,
  editors, agents), shown **read-only** with their tty, remote host, and running
  command. They can't be attached because aether didn't create their PTY.

Each row shows the name/tty, a status (free / in use / external), what's running
(agent type, e.g. `coding agent`), the working directory, age, and remote host. A
two-line status bar at the bottom shows the keys:

| Key            | Action                                               |
|----------------|------------------------------------------------------|
| `Up`/`Dn`, `j`/`k` | Move the selection                               |
| `1`–`9`        | Jump to a session by number                          |
| `Enter`        | Attach to the selected **managed** session; prompts before taking over an in-use session |
| `n`            | Start a new session                                  |
| `t`            | Terminate the selected **managed** session           |
| `Ctrl+t`       | Terminate **all managed** sessions (asks to confirm) |
| `r`            | Restart the shell in the selected **managed** session |
| `Ctrl+r`       | Restart **all managed** sessions (asks to confirm)   |
| `q` / `Esc`    | Quit the login cleanly                               |

External rows ignore attach/kill/restart (a notice explains why). The chooser
uses raw ANSI control sequences — no `tput`/terminfo dependency.

**Switch in place.** When you run `aether --menu`, `aether --attach <name>`, or
`aether --new` *from inside an existing session*, aether moves your current
terminal to the chosen session — like `tmux switch-client` — instead of nesting
a second viewer inside the first. This avoids leaving a hidden client attached to
a session you've navigated away from (which would otherwise keep that session
pinned "in use").

## Layout

```
cmd/aether/main.go       # client — login shell wrapper
cmd/aether-connect/main.go # local-only reconnect transport wrapper
cmd/aetherd/main.go      # daemon — session manager
internal/daemon/         # PTY session lifecycle, daemon server, procfs utilities
internal/daemon/discover_linux.go  # external remote-login discovery (utmp + /proc)
internal/client/         # Unix socket client, chooser UI
internal/client/geometry.go        # terminal geometry capture + cache
internal/proto/          # wire protocol (JSON handshake + framed PTY I/O)
internal/detect/         # agent detection via process tree inspection
```

## Requirements

- Linux with `/proc` filesystem and `/var/run/utmp`
- Go 1.21+ (to build from source)
- Remote-only activation is wired by `/etc/profile.d/aether.sh` (installed by
  `install.sh`) — no `chsh` needed

## Install

```bash
git clone https://github.com/Gwolfgit/aethershell.git
cd aethershell
sudo ./install.sh
```

This builds and installs the remote-host binaries and the connector, but
**changes nothing** about any user's login shell yet.

```bash
# Set aether as your login shell
chsh -s /usr/local/bin/aether $USER
```

For a laptop/workstation that only needs to connect to remote AetherShell hosts,
install just the connector:

```bash
sudo ./install.sh --connector-only
```

That installs `/usr/local/bin/aether-connect` only. It does not install
`aetherd`, systemd units, profile hooks, or a login shell.

## Verify

```bash
getent passwd "$USER" | cut -d: -f7     # which login shell is active
aether --list                            # list your persistent sessions
aether ssh my-server                     # reconnecting client-affined SSH
aether ts my-server                      # reconnecting Tailscale SSH, no sshd required
aether-connect my-server                 # laptop connector, Tailscale SSH by default
aether --takeover shell-20260612-120000  # force-attach a stale busy session
```

## Daemon management

`aetherd` is installed as a systemd user service:

```bash
systemctl --user start aetherd.service
systemctl --user reload aetherd.service      # hot-upgrade without killing sessions
systemctl --user restart aetherd.service     # hard restart; kills existing PTY sessions
systemctl --user status aetherd.service
journalctl --user -u aetherd.service
```

The `aether` login shell wrapper will start this service automatically when it
needs a daemon and no socket exists.

After installing a new `aetherd` binary, prefer:

```bash
aether --upgrade-daemon
```

That execs the new daemon in place and hands off the existing listener and PTY
master file descriptors, so managed shells survive. Existing attached clients
disconnect during the exec and then reconnect to the same sessions.

## Emergency bypass

If you need to force normal interactive logins while keeping `aether` as your
configured login shell, create this file:

```bash
mkdir -p ~/.aethershell
touch ~/.aethershell/disabled
```

Remove the file to re-enable aethershell. You can also set
`AETHER_DISABLE=1` for a single process.

## Uninstall

```bash
sudo ./install.sh --uninstall
```

## Security

AetherShell holds your live terminal, so its trust model matters. In short:

- The daemon is **per-user** and authenticates connections by uid
  (`SO_PEERCRED`) on top of `0700`/`0600` permissions — another local user
  cannot reach your sessions.
- Session metadata shown by `aether --list`/the chooser is sanitized of terminal
  control bytes, so a booby-trapped file/directory name can't inject escape
  sequences into your terminal.
- The `install.sh` login hook is **box-wide**: it routes every user's remote
  interactive SSH login through `aether`. Know the recovery paths
  (`~/.aethershell/disabled`, `AETHER_DISABLE=1`, `ssh host bash -l`, or removing
  `/etc/profile.d/aether.sh`) before enabling it on a shared host.

Read the full threat model, deployment guidance, and known limitations in
**[SECURITY.md](SECURITY.md)**.

## License

MIT — see [LICENSE](LICENSE).
