#!/usr/bin/env bash
# Permissions of socket, runtime dir, restore state, geometry cache.
set -u
. /harness/lib.sh

hr "T01: file & socket permissions"
start_daemon || exit 1

echo "-- runtime dir tree --"
ls -lan "$VICTIM_XDG" "$VICTIM_XDG/aethershell"
echo "-- socket --"
ls -lan "$VICTIM_SOCK"
stat -c '%A %U:%G %n' "$VICTIM_SOCK"

echo "-- create a session, then look for restore/geometry/state files --"
spawn_session_running $'echo hello-from-session\n'
as_victim python3 /harness/probe.py "$VICTIM_SOCK" '{"type":"list"}' || true

echo "-- trigger a hot upgrade to force a restore-*.json to be written --"
as_victim aether --upgrade-daemon 2>&1 || true
sleep 0.5
echo "restore/state files in runtime dir:"
ls -lan "$VICTIM_XDG/aethershell"/ 2>/dev/null
find "$VICTIM_XDG/aethershell" -name 'restore-*.json' -exec sh -c 'echo "== {} =="; ls -lan "{}"; head -c 400 "{}"; echo' \; 2>/dev/null
echo "geometry cache:"
find / -name geometry.json 2>/dev/null -exec ls -lan {} \;

stop_daemon
