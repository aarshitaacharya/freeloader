"""Preload N keys (key:000000000000 ...) with a fixed-size value, pipelined.

Uses plain RESP over a socket so it works against both fl-cache and Redis
without needing commands (like ECHO) that fl-cache doesn't implement.
usage: preload.py PORT NKEYS PAYLOAD_BYTES
"""
import socket
import sys

port, n, payload = int(sys.argv[1]), int(sys.argv[2]), int(sys.argv[3])
value = b"x" * payload
BATCH = 1000

sock = socket.create_connection(("127.0.0.1", port))
buf = b""
for start in range(0, n, BATCH):
    count = min(BATCH, n - start)
    req = b"".join(
        b"*3\r\n$3\r\nSET\r\n$16\r\nkey:%012d\r\n$%d\r\n%s\r\n" % (i, payload, value)
        for i in range(start, start + count)
    )
    sock.sendall(req)
    need = 5 * count  # "+OK\r\n" per command
    got = 0
    while got < need:
        chunk = sock.recv(need - got)
        if not chunk:
            sys.exit("connection closed during preload")
        if chunk[:1] == b"-":
            sys.exit("server error during preload: %r" % chunk)
        got += len(chunk)
sock.close()
