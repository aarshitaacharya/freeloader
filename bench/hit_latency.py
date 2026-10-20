"""Measures cache-HIT latency through a running gateway and asserts p50 < 10 ms.

Run it where the gateway lives (inside the compose network) so Docker Desktop's
host port-forward isn't part of the measurement:

    make check-hit-latency KEY=fl_...

Prints the command's own numbers; nothing here is hard-coded.
"""
import http.client
import json
import os
import statistics
import sys
import time

host, port = os.environ.get("GATEWAY_HOST", "gateway"), int(os.environ.get("GATEWAY_PORT", "8080"))
key, n = os.environ["KEY"], int(os.environ.get("N", "300"))
hdr = {"Authorization": "Bearer " + key, "Content-Type": "application/json"}
body = json.dumps({"model": os.environ.get("MODEL", "fast"), "temperature": 0,
                   "messages": [{"role": "user", "content": "hit-latency probe %d" % time.time()}]})
conn = http.client.HTTPConnection(host, port)


def call():
    t = time.perf_counter()
    conn.request("POST", "/v1/chat/completions", body, hdr)
    r = conn.getresponse()
    r.read()
    return (time.perf_counter() - t) * 1000, r.status, r.getheader("X-Freeloader-Cache")


first = call()
if first[1] != 200:
    sys.exit("first request failed with HTTP %d" % first[1])
runs = [call() for _ in range(n)]
if any(r[2] != "HIT" for r in runs):
    sys.exit("not every repeat was a cache HIT: %s" % sorted({r[2] for r in runs}))
ms = sorted(r[0] for r in runs)
p50, p99 = statistics.median(ms), ms[int(len(ms) * 0.99)]
print(f"first request ({first[2]}): {first[0]:.2f} ms")
print(f"{n} cache HITs: p50={p50:.2f} ms  p99={p99:.2f} ms  max={ms[-1]:.2f} ms")
sys.exit(0 if p50 < 10 else "FAIL: p50 >= 10 ms")
