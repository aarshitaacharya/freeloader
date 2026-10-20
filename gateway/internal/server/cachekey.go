package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
)

// cacheKeyFields are the request fields that can change a completion. The spec
// lists model alias, messages, temperature, max_tokens and tools; the rest are
// included too because ignoring them would serve a wrong cached answer
// (e.g. a different tool_choice or response_format). Fields that don't affect
// the answer ("user", "stream_options", ...) are deliberately left out.
var cacheKeyFields = []string{
	"messages", "temperature", "max_tokens", "max_completion_tokens", "tools", "tool_choice",
	"response_format", "top_p", "stop", "seed", "n", "frequency_penalty", "presence_penalty",
	"reasoning_effort",
}

// CacheKey returns the response-cache key for a request: SHA-256 of a canonical
// JSON form (object keys sorted, numbers normalised), so semantically identical
// requests collide regardless of key order or "0" vs "0.0".
func CacheKey(model string, req map[string]json.RawMessage) (string, error) {
	canon := map[string]any{"model": model}
	for _, f := range cacheKeyFields {
		raw, ok := req[f]
		if !ok {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return "", err
		}
		canon[f] = normalize(v)
	}
	b, err := json.Marshal(canon) // map keys are sorted by encoding/json
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "resp:" + hex.EncodeToString(sum[:]), nil
}

func normalize(v any) any {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return t
		}
		if f == math.Trunc(f) && math.Abs(f) < 1e15 {
			return int64(f)
		}
		return f
	case []any:
		for i := range t {
			t[i] = normalize(t[i])
		}
		return t
	case map[string]any:
		for k := range t {
			t[k] = normalize(t[k])
		}
		return t
	}
	return v
}
