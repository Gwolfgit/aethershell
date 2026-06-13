# AetherShell — Adversarial Security Review

Date: 2026-06-13
Reviewer: live adversarial review (code audit + Dockerized exploit testing)
Scope: the whole repository at branch `security-hardening` (forked from `main`@`1fe4ae2`)
Method: read-as-attacker code audit, then live exploitation against `aetherd`
running inside a disposable Docker container with two unprivileged users
(`victim` running the daemon, `attacker` trying to break in). Harness and raw
wire-protocol probe live under `security-review/harness/`.

## Threat model

`aetherd` is a **per-user** daemon. Each user gets a private instance listening
on a Unix domain socket at `$XDG_RUNTIME_DIR/aethershell/sock` (or
`~/.aethershell/sock`). Sessions are PTY-backed `/bin/bash` processes that
outlive client disconnects. The README states the core security guarantee:

> **Per-user isolation** — each user gets a private daemon instance via Unix
> socket; sessions never leak between users.

Relevant adversaries:

1. **Another local user** (different uid) on the same host trying to observe,
   inject into, hijack, or kill another user's sessions.
2. **A remote party who can influence content** the operator later views —
   file/directory names, repo contents, reverse-DNS — without any account on the
   box.
3. **A hostile or buggy client / network**: malformed protocol, no-newline
   handshakes, oversized frames, half-open floods, broken pipes, stale sockets.
4. **A hostile process running inside the user's own session** (e.g. a coding
   agent or downloaded build script) trying to escalate control of the shell.

Trust assumptions that hold: the socket file mode (`0600`) and the runtime
directory mode (`0700`) are the enforced isolation boundary. Anyone who already
runs code as the user has full access by definition; findings that require
same-uid code execution are scored as safety/robustness, not privilege crossing.

## What holds up

- Socket and runtime-dir permissions are correct by default: `0700` dir, `0600`
  socket, both owned by the user (T01). A different local user is blocked both
  by the `0700` runtime dir (T02-A) and, even in a world-traversable directory,
  by the `0600` socket itself (T02-B).
- `scanUserProcs` strictly filters `/proc` by `uid`, so external-session
  discovery never surfaces another user's processes.
- Malformed JSON and unknown request types are rejected cleanly without crashing
  (T03).
- Command-line construction for the SSH wrapper (`shellQuote`) escapes single
  quotes correctly; the `client_id` cannot break out into shell command
  injection.
- The OSC terminal-title path (`sessiontitle.OSC`) already sanitizes control
  bytes.

## Findings

| ID | Severity | Title | Status |
|----|----------|-------|--------|
| F1 | High     | Per-user isolation relies on a single `chmod`; no peer-credential check | Fixed (SO_PEERCRED + dir chmod), verified live |
| F2 | Medium   | Terminal escape-sequence injection via unsanitized session metadata | Fixed (SanitizeTerminal), verified live |
| F3 | Medium   | PID-reuse: daemon SIGKILLs an arbitrary same-user PID after hot-upgrade | Fixed (start-time guard), tested |
| F4 | Medium   | No handshake read deadline / unbounded connections / pre-allocated frames | Fixed (deadline + conn cap), tested |
| F5 | Medium   | `client_id` is an unauthenticated bearer capability that is broadly exposed | Mitigated by F1; documented in SECURITY.md |
| F6 | Low      | Aggressive global install defaults (intercepts all users' SSH logins) | Documented (install.sh warning + SECURITY.md) |
| F7 | Low      | Stale `restore-*.json` left on a failed hot-upgrade leaks session metadata | Fixed (cleanup on exec failure) |
| F8 | Low      | Outdated `go` directive (1.19, EOL) and pinned x/sys, x/term | `go` bumped to 1.21; dep bump left to maintainer (latest forces go 1.25) |

**Post-fix live verification (Docker harness, rebuilt with the patched code):**
- F1: with the socket forced to `0666` in a traversable dir, the attacker's
  `connect()` now succeeds but the daemon logs `rejecting connection from uid
  2001 (daemon runs as uid 2000)` and serves nothing; the legitimate uid still
  works.
- F2: `aether --list` of a session sitting in a directory with an escape-laden
  name now emits **no ESC bytes** — the OSC/CSI sequences render as inert text.
- F3: a hot-upgrade still preserves live sessions (start-time matches), and the
  Go regression test confirms a recycled PID is neither adopted nor killed.

---

### F1 — Per-user isolation relies on a single `chmod`; no peer-credential check (High)

**Affected:** `internal/daemon/daemon.go` (`Start`, `relisten`, `handleConn`)

**What:** The daemon authenticates nobody. Its entire isolation guarantee is the
socket file mode. `net.Listen("unix", …)` creates the socket with
umask-derived permissions and only *afterwards* `os.Chmod(…, 0600)` tightens it,
and no connection is ever checked against the peer's uid (`SO_PEERCRED`).

**Repro (T02):**
- (A) attacker connecting to the socket in `/run/user/2000` (mode `0700`) → blocked.
- (B) daemon re-homed under a world-traversable `0755` dir; attacker can now
  reach the `0600` socket but `connect()` still fails (needs write) → blocked.
- (C) the socket is `chmod 0666` (a plausible misconfig, a umask-`000` TOCTOU
  window during creation, or any future code regression): the attacker then
  **lists all of the victim's sessions and `kill_all`s them** with no further
  barrier. There is no second layer.

**Impact:** The product's headline security property is single-layer. Any of:
a loosened socket/dir mode, a shared `XDG_RUNTIME_DIR`, an NFS/odd-mount home
fallback, a umask-`000` process during the create→chmod window, or a future
refactor — fully exposes every session of the user to another local user:
read live terminal output, inject keystrokes, take over, or destroy sessions.

**Fix:** Enforce `SO_PEERCRED` on every accepted connection and reject any peer
whose uid is not the daemon's own uid. Additionally create the socket with a
tight umask so it is never briefly world-accessible. (Implemented:
`peercred_linux.go` + umask guard in `Start`/`relisten`; regression test
`peercred_linux_test.go`.)

---

### F2 — Terminal escape-sequence injection via unsanitized session metadata (Medium)

**Affected:** `internal/client/chooser.go` (`renderMenu`, status bars),
`cmd/aether/main.go` (`cmdList`), session naming in
`internal/daemon/daemon.go` (`renameSessionLocked`).

**What:** `aether --list` and the full-screen chooser print session `Name`,
`Agent.WorkDir`, `Agent.Title`/running column, and `RemoteHost` **verbatim** to
the operator's terminal. All of those derive from untrusted sources: a process's
working directory (`/proc/<pid>/cwd`), command line, and the utmp host field
(attacker-influenced via reverse DNS). The OSC-title path sanitizes; these paths
do not.

**Repro (T04):** create a directory whose *name* contains
`␛]2;PWNED-WINDOW-TITLE␛\␛[31m…` (a malicious tarball/repo/upload), have any
session's foreground process sit in it, then run `aether --list`. Captured raw
output contains the ESC bytes verbatim:

```
sleep 1000 in /tmp/^[]2;PWNED-WINDOW-TITLE^[\^[[31mEVILZZZ
```

Merely listing sessions rewrites the operator's terminal title. A real attacker
uses OSC 52 (clipboard write), screen-rewriting, or title-report sequences that
echo back into the input buffer — the last of which is command injection on a
number of terminal emulators.

**Impact:** An unprivileged remote party who can plant a filename/dirname (or
control reverse DNS for the `RemoteHost` column) can inject terminal control
sequences into the operator's terminal the moment the operator inspects their
sessions. Severity is Medium in general and can reach High on terminals
vulnerable to answerback/title-report injection.

**Fix:** Centrally sanitize every human-facing string before it is written to a
terminal (strip C0/C1 control bytes, ESC, DEL), and strip control bytes from
session names at assignment. (Implemented: `internal/proto/sanitize.go`, applied
in chooser/list rendering and in `renameSessionLocked`; tests
`internal/proto/sanitize_test.go`.)

---

### F3 — PID-reuse: daemon SIGKILLs an arbitrary same-user PID after hot-upgrade (Medium)

**Affected:** `internal/daemon/restore.go`, `internal/daemon/session.go`
(`IsAlive`, `killShell`).

**What:** After a hot-upgrade (`aether --upgrade-daemon`), restored sessions are
rebuilt with `cmd == nil` and only a numeric `shellPid`. `IsAlive()` then trusts
`kill(pid,0)` + a zombie check, and `killShell()` falls back to
`syscall.Kill(shellPid, SIGKILL)` with **no verification that the PID is still
the same process**. If the original shell exited and the kernel recycled its PID
to an unrelated same-user process, `pruneDeadSessions`, `handleKill`,
`kill_all`, and `RestartShell` will SIGKILL that unrelated process.

**Repro (Go-level, decisive):** a restore state naming an arbitrary live
`sleep` PID is adopted; `IsAlive()` reports `true`; `killShell()` kills it:

```
adopted session shellPid=3443140 cmd==nil? true
IsAlive (before kill) = true
>>> canary pid 3443140 was KILLED by killShell() using the restore-supplied
    shell_pid (no identity check)
```

**Impact:** Same-user only, so not a privilege crossing, but a hot-upgrade — a
normal, advertised operation — can cause the daemon to kill an unrelated process
of the user (a long-running build, an editor, an SSH session). Unexpected
control of the user's processes is explicitly in scope.

**Fix:** Capture the shell's process start-time (`/proc/<pid>/stat` field 22)
into the restore state and verify it before treating a PID as the session's
shell or killing it. (Implemented: `proc_linux.go` start-time helpers; restore
state carries `start_ticks`; `IsAlive`/`killShell` verify identity;
regression test.)

---

### F4 — No handshake read deadline / unbounded connections / pre-allocated frames (Medium)

**Affected:** `internal/daemon/daemon.go` (`handleConn`, `acceptLoop`),
`internal/proto/frame.go` (`ReadFrame`).

**What / Repro (T03):**
- A connection that never sends a newline parks the `handleConn` goroutine on
  `ReadBytes('\n')` forever — there is no read deadline.
- 200 silent connections were opened, each parking a goroutine/fd; nothing
  bounds the count.
- `ReadFrame` does `make([]byte, n)` for `n` up to 16 MiB *before* the body
  arrives, so each connection can pin 16 MiB by sending only a 5-byte header.

**Impact:** Local same-user resource exhaustion, and a wedged/buggy transport can
accumulate parked goroutines/fds over time. Not a cross-user issue but a
robustness/DoS gap for a daemon meant to run unattended for weeks.

**Fix:** Apply a read deadline to the JSON handshake; cap concurrent
connections; clear the deadline once a stream is established.
(Implemented in `handleConn` + a connection semaphore.)

---

### F5 — `client_id` is an unauthenticated bearer capability that is broadly exposed (Medium)

**Affected:** `internal/connect/connect.go` (`remoteLoginCommand`),
`internal/daemon/daemon.go` (`handleAttachClient`, `ClaimAttachment`).

**What / Repro (T06):** `attach_client` looks up `affinity[client_id]` and hands
over the live session — no `force`, no session name, booting the previous
client. `client_id` is therefore a bearer token, yet it is (a) passed on the SSH
*remote command line* (`AETHERSHELL_CLIENT_ID='…' exec aether --login`), visible
in `/proc/<pid>/cmdline` to other local users when `hidepid=0` (the default),
and (b) exported into the session environment, inherited by every child process.

**Impact:** Anyone who learns a `client_id` can silently and stealthily take over
that client's session — today gated only by socket access (so same-uid), but it
removes the need for `force` and is the natural escalation if F1's socket
boundary is ever bypassed.

**Fix:** Real authentication is the F1 peer-cred check; `client_id` should be
treated as a non-secret routing hint, not a credential. Documented in
`SECURITY.md`; recommend passing it via SSH `SetEnv`/`SendEnv` rather than the
command line in a future change.

---

### F6 — Aggressive global install defaults (Low)

**Affected:** `install.sh`.

`install.sh` writes `/etc/profile.d/aether.sh` (intercepts **every** user's
remote interactive SSH login) and runs `systemctl --global enable`. For a public
project this is a surprising, box-wide default and a login-availability risk if
`aether`/`aetherd` misbehave. There is an escape hatch
(`~/.aethershell/disabled`, `AETHER_DISABLE=1`, or any non-interactive
`ssh host <cmd>`), but it is undocumented at the point of risk.

**Fix:** Document the blast radius and recovery prominently (SECURITY.md +
README), and clearly flag the global hook during install.

---

### F7 — Stale `restore-*.json` on a failed hot-upgrade leaks session metadata (Low)

**Affected:** `internal/daemon/daemon.go` (`prepareUpgrade`,`handleUpgrade`).

On a *successful* upgrade `Restore` removes the state file. If the `syscall.Exec`
fails after the file is written, the file lingers in the runtime dir containing
session names and `client_id`s. It is `0600` in a `0700` dir, so exposure is
limited to the user, but it is unnecessary residue.

**Fix:** Best-effort remove the state file if `exec` returns.

---

### F8 — Outdated toolchain directive and dependencies (Low)

`go.mod` declares `go 1.19` (EOL since ~2023-09). `golang.org/x/sys` and
`golang.org/x/term` are pinned at `v0.13.0` (2023). No known critical CVE
applies, but bumping keeps current security fixes flowing.

**Fix:** Recommend bumping the `go` directive and the `x/` modules.
