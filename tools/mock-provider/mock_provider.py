"""A fake OpenAI-compatible provider for offline development and drills.

Behaviour is chosen by the first path segment, so one process can play several providers:

    POST /ok/v1/chat/completions      200, answers "mock reply from ok"
    POST /broken/v1/chat/completions  401 (a revoked key)
    POST /limited/v1/chat/completions 429
    POST /down/v1/chat/completions    503
    POST /slow/v1/chat/completions    sleeps 5 s, then 200
    GET  /_stats                      {"ok": 3, "broken": 1, ...}  requests seen per provider
    POST /_reset                      zero the counters

Every success reports usage of 10 prompt + 5 completion tokens.
"""
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

STATS = {}
LOCK = threading.Lock()

ERRORS = {"broken": 401, "limited": 429, "down": 503}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_):
        pass

    def _send(self, code, body, extra=None):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        for k, v in (extra or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path == "/_stats":
            with LOCK:
                return self._send(200, dict(STATS))
        self._send(404, {"error": "not found"})

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        raw = self.rfile.read(length) if length else b"{}"
        if self.path == "/_reset":
            with LOCK:
                STATS.clear()
            return self._send(200, {"ok": True})

        parts = self.path.strip("/").split("/")  # name, v1, chat, completions
        if len(parts) != 4 or parts[1:] != ["v1", "chat", "completions"]:
            return self._send(404, {"error": {"message": "not found"}})
        name = parts[0]
        with LOCK:
            STATS[name] = STATS.get(name, 0) + 1

        if name in ERRORS:
            extra = {"Retry-After": "30"} if ERRORS[name] == 429 else None
            return self._send(ERRORS[name], {"error": {"message": f"mock {name}", "code": ERRORS[name]}}, extra)
        if name == "slow":
            time.sleep(5)
        try:
            model = json.loads(raw).get("model", "?")
        except ValueError:
            return self._send(400, {"error": {"message": "invalid json"}})
        self._send(200, {
            "id": "chatcmpl-mock", "object": "chat.completion", "created": int(time.time()), "model": model,
            "choices": [{"index": 0, "finish_reason": "stop",
                         "message": {"role": "assistant", "content": f"mock reply from {name} ({model})"}}],
            "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
        })


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 9000), Handler).serve_forever()
