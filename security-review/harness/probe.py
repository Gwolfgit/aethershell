#!/usr/bin/env python3
"""Raw aethershell wire-protocol probe for adversarial testing.

Implements just enough of the protocol to abuse it:
  - send an arbitrary JSON request line
  - read the newline-delimited handshake
  - read/write the length-prefixed frame stream

Frame: [1 byte type][4 byte big-endian len][payload]
  type 'd' = data, 'r' = resize (json Control), 'x' = detach
"""
import json, os, socket, struct, sys, time

def connect(path):
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.connect(path)
    return s

def send_request(s, req):
    s.sendall((json.dumps(req) + "\n").encode())

def read_line(s, timeout=2.0):
    s.settimeout(timeout)
    buf = b""
    try:
        while not buf.endswith(b"\n"):
            chunk = s.recv(1)
            if not chunk:
                break
            buf += chunk
    except socket.timeout:
        pass
    return buf

def read_frame(s, timeout=2.0):
    s.settimeout(timeout)
    hdr = b""
    while len(hdr) < 5:
        chunk = s.recv(5 - len(hdr))
        if not chunk:
            return None, None
        hdr += chunk
    typ = hdr[0:1]
    n = struct.unpack(">I", hdr[1:5])[0]
    payload = b""
    while len(payload) < n:
        chunk = s.recv(min(65536, n - len(payload)))
        if not chunk:
            break
        payload += chunk
    return typ, payload

def write_frame(s, typ, payload=b""):
    s.sendall(typ + struct.pack(">I", len(payload)) + payload)

def cmd_list(path):
    s = connect(path)
    send_request(s, {"type": "list"})
    line = read_line(s)
    print(line.decode(errors="replace"))
    s.close()

if __name__ == "__main__":
    # Generic CLI: probe.py <socket> <json-request> [--frames]
    sock = sys.argv[1]
    req = json.loads(sys.argv[2])
    s = connect(sock)
    send_request(s, req)
    line = read_line(s)
    sys.stdout.write("HANDSHAKE: " + line.decode(errors="replace"))
    if "--frames" in sys.argv:
        for _ in range(3):
            typ, payload = read_frame(s, timeout=1.0)
            if typ is None:
                break
            sys.stdout.write("FRAME %r len=%d\n" % (typ, len(payload)))
    s.close()
