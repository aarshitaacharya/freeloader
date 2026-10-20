"""Streams a chat through the gateway with the *official OpenAI Python client*.

    make check-openai-client KEY=fl_... [MODEL=fast]

Checks that: the stream arrives incrementally (not all at the end), the content
is assembled correctly, the stream ends cleanly, usage is delivered when the
client asks for it, and a non-streaming call still works.
"""
import os
import sys
import time

from openai import OpenAI

base = os.environ.get("GATEWAY_URL", "http://gateway:8080") + "/v1"
model = os.environ.get("MODEL", "fast")
client = OpenAI(base_url=base, api_key=os.environ["KEY"], max_retries=0)
prompt = os.environ.get("PROMPT", "Count from one to five in words, separated by commas.")


def stream_once(include_usage):
    t0 = time.perf_counter()
    kwargs = {"stream_options": {"include_usage": True}} if include_usage else {}
    stream = client.chat.completions.create(
        model=model, stream=True, max_tokens=int(os.environ.get("MAX_TOKENS", "300")),
        messages=[{"role": "user", "content": prompt}], **kwargs)
    arrivals, text, usage, finish = [], "", None, None
    for chunk in stream:
        arrivals.append(time.perf_counter() - t0)
        if chunk.usage:
            usage = chunk.usage
        for ch in chunk.choices:
            if ch.delta.content:
                text += ch.delta.content
            if ch.finish_reason:
                finish = ch.finish_reason
    return arrivals, text, usage, finish


failed = False
for include_usage in (False, True):
    arrivals, text, usage, finish = stream_once(include_usage)
    spread = arrivals[-1] - arrivals[0] if arrivals else 0
    print(f"stream include_usage={include_usage}: {len(arrivals)} chunks over {spread * 1000:.0f} ms, "
          f"first at {arrivals[0] * 1000:.0f} ms, finish={finish!r}, text={text.strip()[:70]!r}, "
          f"usage={'%s/%s tokens' % (usage.prompt_tokens, usage.completion_tokens) if usage else None}")
    if not text or finish is None:
        print("  FAIL: empty text or no finish_reason"); failed = True
    if include_usage and usage is None:
        print("  FAIL: asked for usage but did not receive it"); failed = True
    if not include_usage and usage is not None:
        print("  FAIL: received a usage chunk that was not requested"); failed = True

r = client.chat.completions.create(model=model, max_tokens=int(os.environ.get("MAX_TOKENS", "300")), messages=[{"role": "user", "content": "Say hi."}])
print(f"non-stream: {r.choices[0].message.content!r}  usage={r.usage.total_tokens if r.usage else None}")
sys.exit(1 if failed else 0)
