#!/usr/bin/env bash
# Malformed / hostile protocol input.
set -u
. /harness/lib.sh

hr "T03: malformed protocol input"
start_daemon || exit 1

echo "-- bad JSON request --"
as_victim python3 /harness/probe.py "$VICTIM_SOCK" '"not-an-object"' 2>&1 | head -3 || true

echo "-- unknown request type --"
as_victim python3 /harness/probe.py "$VICTIM_SOCK" '{"type":"frobnicate"}' 2>&1 | head -3 || true

echo "-- giant frame length header (claims 16MiB, sends nothing) --"
as_victim python3 - "$VICTIM_SOCK" <<'PY' 2>&1 | head -5 || true
import socket,struct,sys,json,time
s=socket.socket(socket.AF_UNIX);s.connect(sys.argv[1])
s.sendall((json.dumps({"type":"create","client_id":"giant"})+"\n").encode())
s.settimeout(2)
try: s.recv(4096)
except Exception as e: print("recv after create:",e)
# send a frame header claiming a huge payload but send no body
s.sendall(b"d"+struct.pack(">I", 16*1024*1024))
time.sleep(1)
print("daemon still alive after dangling giant-frame header (no crash)")
PY

echo "-- connect, send NO newline, hold open (handshake-read hang / goroutine leak) --"
as_victim python3 - "$VICTIM_SOCK" <<'PY' 2>&1 | head -5 || true
import socket,sys,time
s=socket.socket(socket.AF_UNIX);s.connect(sys.argv[1])
s.sendall(b'{"type":"list"')  # no newline, ever
time.sleep(1.5)
print("held a connection open with no newline for 1.5s (no server-side timeout?)")
s.close()
PY

echo "-- many half-open connections (resource pressure) --"
as_victim python3 - "$VICTIM_SOCK" <<'PY' 2>&1 | head -5 || true
import socket,sys
conns=[]
for i in range(200):
    try:
        s=socket.socket(socket.AF_UNIX);s.connect(sys.argv[1]);s.sendall(b"x")
        conns.append(s)
    except Exception as e:
        print("stopped at",i,e);break
print("opened",len(conns),"silent connections; daemon goroutines now parked on handshake read")
PY

echo "-- daemon still serving legit clients? --"
as_victim python3 /harness/probe.py "$VICTIM_SOCK" '{"type":"list"}' 2>&1 | head -3 || true
echo "daemon log tail:"; tail -5 "$DAEMON_LOG"

stop_daemon
