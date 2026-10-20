package server

import (
	"encoding/json"
	"testing"
)

func req(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func key(t *testing.T, model, body string) string {
	t.Helper()
	k, err := CacheKey(model, req(t, body))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestCacheKeyIgnoresFormattingAndKeyOrder(t *testing.T) {
	a := key(t, "fast", `{"model":"fast","temperature":0,"messages":[{"role":"user","content":"hi"}],"max_tokens":50}`)
	b := key(t, "fast", `{
		"max_tokens": 50,
		"messages": [ {"content": "hi", "role": "user"} ],
		"temperature": 0.0,
		"model": "fast"
	}`)
	if a != b {
		t.Errorf("equivalent requests got different keys:\n%s\n%s", a, b)
	}
}

func TestCacheKeyIgnoresFieldsThatDoNotAffectTheAnswer(t *testing.T) {
	a := key(t, "fast", `{"messages":[{"role":"user","content":"hi"}],"temperature":0}`)
	b := key(t, "fast", `{"messages":[{"role":"user","content":"hi"}],"temperature":0,"user":"u1","stream":false,"metadata":{"x":1}}`)
	if a != b {
		t.Error("user/stream/metadata must not change the key")
	}
}

func TestCacheKeyDistinguishesWhatMatters(t *testing.T) {
	base := `{"messages":[{"role":"user","content":"hi"}],"temperature":0,"max_tokens":50}`
	baseKey := key(t, "fast", base)
	variants := map[string]string{
		"model alias":   key(t, "smart", base),
		"content":       key(t, "fast", `{"messages":[{"role":"user","content":"hello"}],"temperature":0,"max_tokens":50}`),
		"role":          key(t, "fast", `{"messages":[{"role":"system","content":"hi"}],"temperature":0,"max_tokens":50}`),
		"temperature":   key(t, "fast", `{"messages":[{"role":"user","content":"hi"}],"temperature":0.2,"max_tokens":50}`),
		"max_tokens":    key(t, "fast", `{"messages":[{"role":"user","content":"hi"}],"temperature":0,"max_tokens":51}`),
		"tools":         key(t, "fast", `{"messages":[{"role":"user","content":"hi"}],"temperature":0,"max_tokens":50,"tools":[{"type":"function"}]}`),
		"tool_choice":   key(t, "fast", `{"messages":[{"role":"user","content":"hi"}],"temperature":0,"max_tokens":50,"tool_choice":"none"}`),
		"message order": key(t, "fast", `{"messages":[{"role":"user","content":"a"},{"role":"user","content":"hi"}],"temperature":0,"max_tokens":50}`),
	}
	seen := map[string]string{baseKey: "base"}
	for name, k := range variants {
		if prev, dup := seen[k]; dup {
			t.Errorf("%q collides with %q", name, prev)
		}
		seen[k] = name
	}
}

func TestCacheKeyShape(t *testing.T) {
	k := key(t, "fast", `{"messages":[]}`)
	if len(k) != len("resp:")+64 || k[:5] != "resp:" {
		t.Errorf("unexpected key %q", k)
	}
}

func TestCacheKeyRejectsInvalidField(t *testing.T) {
	m := map[string]json.RawMessage{"messages": json.RawMessage(`{not json`)}
	if _, err := CacheKey("fast", m); err == nil {
		t.Error("expected an error for unparseable field")
	}
}
