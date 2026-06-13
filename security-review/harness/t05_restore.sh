#!/usr/bin/env bash
# Restore path trusts shell_pid from the state file with NO identity check.
# Combined with PID reuse after a hot-upgrade, this lets the daemon SIGKILL an
# unrelated same-user process. Here we prove the mechanism directly.
set -u
. /harness/lib.sh

hr "T05: restore trusts shell_pid -> SIGKILLs whatever PID it names"

as_victim sleep 4000 &
CANARY=$!
sleep 0.3
echo "victim canary PID: $CANARY (alive? $(kill -0 $CANARY 2>/dev/null && echo yes || echo no))"
echo "this PID is NOT an aether shell — it just happens to be the value in the file"

LISTEN=/tmp/restore-listener.sock
CRAFT=/tmp/evil-restore.json
cat > "$CRAFT" <<JSON
{"socket_path":"$LISTEN","order":["evil"],"affinity":{},
 "sessions":[{"name":"evil","created_unix":0,"client_id":"",
   "last_unix":0,"shell_pid":$CANARY,"geometry":{"Rows":24,"Cols":80},"pty_fd":2}]}
JSON
chown victim "$CRAFT"

echo "-- start restored daemon with a valid inherited listener fd --"
setsid runuser -u victim -- env XDG_RUNTIME_DIR=$VICTIM_XDG \
    python3 /harness/restore_exec.py "$LISTEN" "$CRAFT" >/tmp/restore.log 2>&1 </dev/null &
for _ in $(seq 1 50); do [ -S "$LISTEN" ] && break; sleep 0.1; done
echo "restore log:"; head -10 /tmp/restore.log
echo "sessions the restored daemon now believes it owns:"
as_victim python3 /harness/probe.py "$LISTEN" '{"type":"list"}' 2>&1 | head -3

echo "-- ask it to kill_all (iterates sessions -> SIGKILL shell_pid) --"
as_victim python3 /harness/probe.py "$LISTEN" '{"type":"kill_all"}' 2>&1 | head -3
sleep 0.5
if kill -0 $CANARY 2>/dev/null; then
    echo ">>> canary $CANARY still alive"
else
    echo ">>> canary $CANARY was SIGKILLed by the daemon purely because the restore file named its PID"
fi

kill $CANARY 2>/dev/null || true
pkill -u victim aetherd 2>/dev/null || true
rm -f "$CRAFT" "$LISTEN"
