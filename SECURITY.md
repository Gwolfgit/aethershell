# AetherShell security model

AetherShell keeps your interactive shell alive across SSH disconnects. Because
it holds your live terminal — keystrokes, command output, and any secrets that
pass through it — its security properties matter. This document states what
AetherShell does and does not protect against, how to deploy it safely, and its
known limitations. A full adversarial review and its reproducible test harness
live under [`security-review/`](security-review/).

## Architecture recap

- `aetherd` is a **per-user** daemon. Each user runs their own instance; there
  is no shared/root daemon.
- It listens on a Unix-domain socket at `$XDG_RUNTIME_DIR/aethershell/sock`
  (typically `/run/user/<uid>/aethershell/sock`), falling back to
  `~/.aethershell/sock`.
- Sessions are PTY-backed `/bin/bash` processes. The daemon holds the PTY master
  so the shell survives client disconnects.
- The `aether` client connects to the socket and streams terminal I/O.

## Trust boundary

The security boundary is the **Unix-domain socket**, protected by three layers:

1. The runtime directory is mode `0700`, owned by the user.
2. The socket file is mode `0600`, owned by the user.
3. The daemon enforces `SO_PEERCRED`: it rejects any connection whose peer uid is
   not the daemon's own uid, regardless of file permissions.

Layer 3 is defense in depth added after the security review: even if the socket
or directory mode is loosened (a shared `XDG_RUNTIME_DIR`, an odd home mount, a
misconfiguration, or a future regression), a different local user still cannot
list, attach to, inject into, or kill your sessions.

## What AetherShell protects against

- **Another local user** reading, hijacking, injecting into, or destroying your
  sessions. Blocked by all three layers above.
- **Terminal escape injection** through session metadata. Session names, working
  directories, command lines, and remote-host labels are derived from untrusted
  sources (`/proc`, utmp). They are sanitized of control bytes before being
  rendered by `aether --list` and the chooser, so a directory or file with a
  booby-trapped name cannot inject escape sequences into your terminal when you
  list or choose sessions.
- **Malformed / hostile clients**: bad JSON and unknown requests are rejected;
  oversized frames are bounded; a connection that never completes the handshake
  is dropped after 15s; the number of concurrent connections is capped.
- **PID reuse after a hot-upgrade**: the daemon records each shell's process
  start-time and verifies it before treating a PID as the session's shell or
  signalling it, so it never kills an unrelated same-user process whose PID was
  recycled.
- **Command injection via the connect wrapper**: the `client_id` passed to the
  remote login command is shell-quoted safely.

## What AetherShell does NOT protect against

- **Root, or any code already running as your user.** Anyone who can run code as
  you can read your `/proc`, your socket, and your terminals by definition.
  AetherShell adds no new privilege boundary against yourself; findings that
  require same-uid code execution are robustness/safety issues, not isolation
  breaks.
- **A compromised terminal emulator.** AetherShell strips control bytes from the
  metadata it renders, but it cannot fix a terminal that mishandles sequences in
  the *live PTY stream* (which is, by design, passed through raw).
- **`client_id` confidentiality.** `client_id` is a routing hint, **not** a
  secret. It is passed on the SSH remote command line (visible in
  `/proc/<pid>/cmdline` to other local users when `hidepid=0`) and exported into
  the session environment. Anyone who can already connect to your socket and
  learns a `client_id` can reattach as that client. Authentication is the
  `SO_PEERCRED` uid check, not `client_id`.
- **Network confidentiality/integrity.** AetherShell does not transport anything
  over the network itself — it relies entirely on your SSH/Tailscale SSH
  transport for that. Use a properly configured SSH server.
- **Persistence of secrets in memory.** Up to 1 MiB of recent terminal output
  per session is held in daemon memory for scrollback replay (never written to
  disk). Treat a long-running daemon's memory as containing recent terminal
  contents.

## Safe deployment

- **Prefer `XDG_RUNTIME_DIR`.** Run the daemon under a real user session so the
  socket lives in `/run/user/<uid>` (mode `0700`, managed by systemd-logind).
  The home-directory fallback works but depends on home-directory permissions.
- **Do not share `XDG_RUNTIME_DIR` between users.** The `SO_PEERCRED` check will
  still reject cross-user connections, but a shared runtime dir is a smell.
- **Keep `/proc` hardened.** Mounting `/proc` with `hidepid=2` reduces what other
  local users can learn about your processes (including `client_id` on the SSH
  command line). This is a host setting, not something AetherShell controls.
- **Understand the login hook's blast radius.** `install.sh` installs
  `/etc/profile.d/aether.sh`, which routes **every** user's *remote interactive*
  SSH login through `aether`, and runs `systemctl --global enable`. This is a
  box-wide change. If `aether`/`aetherd` misbehave, interactive SSH logins could
  be affected. Recovery options (any of):
  - non-interactive access bypasses the hook: `ssh host bash -l`
  - per-user disable: `touch ~/.aethershell/disabled`
  - per-session disable: `AETHER_DISABLE=1`
  - remove the hook: `rm /etc/profile.d/aether.sh` (or `./install.sh --uninstall`)
- **Review before enabling for all users.** For a multi-user box, consider
  installing with `--connector-only` on clients and enabling the hook
  selectively rather than globally.

## Known limitations

- The PID-reuse guard verifies start-time only when it was recorded. A restore
  state hand-crafted to omit it skips the check — but crafting that state already
  requires running `aetherd --restore` as the user, i.e. same-uid code execution.
- `kill_all`, `restart_all`, and `upgrade` are available to any client that
  passes the uid check (i.e. you). There is no second confirmation at the
  protocol layer; the chooser confirms destructive actions in the UI.
- AetherShell is Linux-only (it relies on `/proc`, utmp, and `SO_PEERCRED`).

## Reporting a vulnerability

Please open a private report via the repository's security advisory feature, or
contact the maintainer directly. Include reproduction steps; the harness under
`security-review/` is a good starting point.
