#!/usr/bin/env python3
"""Create an aethershell session and inject keystrokes, then hold the stream
open so the session stays alive/attached for inspection by other tools."""
import json, socket, struct, sys, time

sock_path, keys = sys.argv[1], sys.argv[2]
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sock_path)
s.sendall((json.dumps({"type": "create", "client_id": "driver", "rows": 40, "cols": 120}) + "\n").encode())

# read handshake line
buf = b""
s.settimeout(3)
while not buf.endswith(b"\n"):
    c = s.recv(1)
    if not c:
        break
    buf += c

# send keystrokes as a data frame
payload = keys.encode()
s.sendall(b"d" + struct.pack(">I", len(payload)) + payload)

# drain output, keep alive
s.settimeout(1)
end = time.time() + 600
while time.time() < end:
    try:
        s.recv(65536)
    except socket.timeout:
        pass
    except OSError:
        break
