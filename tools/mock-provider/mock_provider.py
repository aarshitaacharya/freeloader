"""A fake OpenAI-compatible provider for offline development and drills.

Behaviour is chosen by the first path segment, so one process can play several providers:

    POST /ok/v1/chat/completions      200, answers "mock reply from ok"
    POST /broken/v1/chat/completions  401 (a revoked key)
    POST /limited/v1/chat/completions 429
    POST /down/v1/chat/completions    503
    POST /slow/v1/chat/completions    sleeps 5 s, then 200
    POST /nousage/v1/chat/completions never reports usage (answers as "ok" otherwise)
    POST /cutoff/v1/chat/completions  streams two chunks, then drops the connection (no [DONE])
    GET  /_stats                      {"ok": 3, "broken": 1, ...}  requests seen per provider
    POST /_reset                      zero the counters

With "stream": true the reply is server-sent events: five word chunks 50 ms apart, then (if
stream_options.include_usage) a final usage chunk with no choices, then [DONE].

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
            req = json.loads(raw)
        except ValueError:
            return self._send(400, {"error": {"message": "invalid json"}})
        model = req.get("model", "?")
        if req.get("stream"):
            return self._stream(name, model, bool((req.get("stream_options") or {}).get("include_usage")))
        usage = None if name == "nousage" else {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
        body = {
            "id": "chatcmpl-mock", "object": "chat.completion", "created": int(time.time()), "model": model,
            "choices": [{"index": 0, "finish_reason": "stop",
                         "message": {"role": "assistant", "content": f"mock reply from {name} ({model})"}}],
        }
        if usage:
            body["usage"] = usage
        self._send(200, body)

    def _stream(self, name, model, include_usage):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()

        def emit(payload):
            data = payload if isinstance(payload, str) else json.dumps(payload)
            frame = f"data: {data}\n\n".encode()
            self.wfile.write(b"%x\r\n%s\r\n" % (len(frame), frame))
            self.wfile.flush()

        def chunk(delta, finish=None):
            return {"id": "chatcmpl-mock", "object": "chat.completion.chunk", "model": model,
                    "choices": [{"index": 0, "delta": delta, "finish_reason": finish}]}

        words = ["mock", " reply", " from", f" {name}", "."]
        emit(chunk({"role": "assistant", "content": ""}))
        for i, w in enumerate(words):
            time.sleep(0.05)
            emit(chunk({"content": w}))
            if name == "cutoff" and i == 1:
                self.close_connection = True
                return  # drop the connection mid-stream: no final chunk, no [DONE]
        emit(chunk({}, "stop"))
        if include_usage and name != "nousage":
            emit({"id": "chatcmpl-mock", "object": "chat.completion.chunk", "model": model, "choices": [],
                  "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
        emit("[DONE]")
        self.wfile.write(b"0\r\n\r\n")
        self.wfile.flush()


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 9000), Handler).serve_forever()
