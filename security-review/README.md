# AetherShell adversarial test harness

Disposable Docker harness used for the security review in [`FINDINGS.md`](FINDINGS.md).
It runs `aetherd` as an unprivileged `victim` user inside a container and tries
to break in as a separate `attacker` user, plus exercises malformed clients,
escape-sequence injection, hostile restore state, and `client_id` takeover.

Everything stays inside a throwaway container — nothing touches the host.

## Build

The image needs the repository source under a `src/` subdirectory of the build
context (the `Dockerfile` does `COPY src/ /build/`):

```bash
work=$(mktemp -d)
mkdir -p "$work/src"
rsync -a --exclude .git --exclude bin /path/to/aethershell/ "$work/src/"
cp -r security-review/Dockerfile "$work/"
cp -r security-review/harness "$work/"
docker build -t aethersec "$work"
```

## Run

```bash
docker run -d --name aethersec aethersec sleep infinity
for t in t01_perms t02_crossuser t03_malformed t04_escape t05_restore t06_clientid; do
  docker exec aethersec bash /harness/$t.sh
done
docker rm -f aethersec
```

## Files

| File | What it probes |
|------|----------------|
| `lib.sh` | shared helpers (start/stop daemon as victim, run as victim/attacker) |
| `probe.py` | raw wire-protocol client: send any JSON request, read frames |
| `drive.py` | create a session and inject keystrokes, hold it open |
| `restore_exec.py` | supply a real listener fd, then `exec aetherd --restore` |
| `t01_perms.sh` | socket / runtime-dir / restore / geometry permissions |
| `t02_crossuser.sh` | cross-user isolation (F1) — includes the loosened-socket case |
| `t03_malformed.sh` | bad JSON, giant frames, no-newline hang, half-open floods (F4) |
| `t04_escape.sh` | terminal escape injection via session metadata (F2) |
| `t05_restore.sh` | hostile restore state, PID targeting (F3) |
| `t06_clientid.sh` | `client_id` bearer-capability takeover (F5) |

After applying the fixes, `t02_crossuser.sh` scenario (C) should show the
attacker **rejected** even when the socket is `chmod 0666`, and `t04_escape.sh`
should report **no ESC bytes** in `aether --list` output.
