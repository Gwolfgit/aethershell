# Changelog

All notable changes to **aethershell** are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [2.0.0] - 2026-06-09

### Changed
- **Complete Go rewrite.** The v1 bash/tmux implementation has been retired.
  aethershell is now a pure Go client/daemon pair with zero external runtime
  dependencies.
- `aetherd` manages PTY sessions directly using `creack/pty`. Sessions persist
  because the PTY master fd stays open in the daemon after client disconnect.
- `aether` receives the PTY fd via Unix domain socket ancillary data
  (`SCM_RIGHTS`) and pipes I/O between stdin/stdout and the PTY.
- Agent detection via procfs scanning (`/proc`), replacing the old
  `@aether_title` tmux session option mechanism.

### Removed
- `bin/persistent-shell` — v1 bash/tmux login wrapper (replaced by `cmd/aether`)
- `bin/persistent-shell-test` — v1 safe trial mode (no longer needed)
- `etc/tmux-persistent-shell.conf` — tmux configuration
- `etc/aether-session.bashrc` — tmux title-capture bashrc

### Added
- Interactive multi-session chooser with live agent detection (raw ANSI, no
  terminfo dependency).
- Session management via the chooser: attach, create, kill, kill-all, restart,
  restart-all.

## [1.0.0] - 2026-06-06

### Added
- `bin/persistent-shell` — login-shell wrapper that drops interactive logins
  into an invisible, per-user tmux session (private socket `persistent-$USER`).
- `bin/persistent-shell-test` — safe 5-minute trial CLI with `enable`,
  `confirm`, `rollback`, and `status`; automatic rollback via `systemd-run`,
  falling back to `at`, then a `nohup sleep` background job.
- `etc/tmux-persistent-shell.conf` — no-chrome tmux config: status bar off,
  no prefix, all keys unbound except **F11** (destroy session) and **F12**
  (restart shell); mouse-wheel scrollback, 200k-line history, terminal-friendly
  pass-through for `vim`/`nano`/`htop`/`less`.
- `install.sh` — installer with `--uninstall` support; registers the wrapper in
  `/etc/shells` when present.
- README and MIT license.

### Behavior
- Login attaches only to **free** (detached) sessions: none free → create one;
  one free → attach; several free → numbered menu (sessions / New / Exit).
  Sessions already in use by another live connection are never mirrored — a new
  login gets its own session instead.
- Non-interactive invocations (no TTY, or a command argument) `exec /bin/bash`,
  so `scp`, `rsync`, `sftp`, and `ssh host <cmd>` are unaffected; no nesting when
  already inside tmux.
- Does not modify `sshd` configuration; works purely as a login shell (suited to
  Tailscale SSH).

[2.0.0]: https://github.com/Gwolfgit/aethershell/compare/v1.0.0...v2.0.0
[1.0.0]: https://github.com/Gwolfgit/aethershell/releases/tag/v1.0.0
