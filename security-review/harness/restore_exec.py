#!/usr/bin/env python3
# Provide a REAL listening unix-socket fd, then exec `aetherd --restore <state>`
# with AETHERD_RESTORE_FD pointing at it — mimicking the hot-upgrade handoff.
import socket, os, sys
listen_path, state = sys.argv[1], sys.argv[2]
try: os.unlink(listen_path)
except FileNotFoundError: pass
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.bind(listen_path); s.listen(16)
fd = s.fileno(); os.set_inheritable(fd, True)
os.environ["AETHERD_RESTORE_FD"] = str(fd)
os.execvp("aetherd", ["aetherd", "--restore", state])
