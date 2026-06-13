#!/usr/bin/env bash
# Cross-user isolation: can 'attacker' reach 'victim's daemon?
# Tests the README claim "sessions never leak between users".
set -u
. /harness/lib.sh

hr "T02: cross-user isolation"
start_daemon || exit 1
spawn_session_running $'echo secret-output-12345\n'

echo "-- (A) realistic: attacker connects to victim socket in /run/user/2000 (0700) --"
as_attacker python3 /harness/probe.py "$VICTIM_SOCK" '{"type":"list"}' 2>&1 | head -5
echo "exit: ${PIPESTATUS[0]}"

echo
echo "-- (B) last line of defense: socket placed in a world-traversable dir --"
echo "   (re-home the daemon under /shared, which is 0755, socket stays 0600)"
stop_daemon
install -d -m 0755 /shared
install -d -m 0755 -o victim -g victim /shared/aethershell
SHARED_XDG=/shared
setsid runuser -u victim -- env XDG_RUNTIME_DIR=$SHARED_XDG aetherd >"$DAEMON_LOG" 2>&1 </dev/null &
for _ in $(seq 1 50); do [ -S /shared/aethershell/sock ] && break; sleep 0.1; done
ls -lan /shared/aethershell/
echo "attacker can traverse the dir now; try to connect to the 0600 socket:"
as_attacker python3 /harness/probe.py /shared/aethershell/sock '{"type":"list"}' 2>&1 | head -5
echo "exit: ${PIPESTATUS[0]}"

echo
echo "-- (C) demonstrate NO second layer: if socket were 0666, attacker is in --"
chmod 0666 /shared/aethershell/sock
ls -lan /shared/aethershell/sock
echo "attacker lists victim sessions:"
as_attacker python3 /harness/probe.py /shared/aethershell/sock '{"type":"list"}' 2>&1 | head -5
echo "attacker kills ALL victim sessions:"
as_attacker python3 /harness/probe.py /shared/aethershell/sock '{"type":"kill_all"}' 2>&1 | head -5

pkill -u victim aetherd 2>/dev/null || true
rm -rf /shared
