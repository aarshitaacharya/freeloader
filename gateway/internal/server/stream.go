package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/aarshitaacharya/freeloader/gateway/internal/auth"
	"github.com/aarshitaacharya/freeloader/gateway/internal/config"
	"github.com/aarshitaacharya/freeloader/gateway/internal/provider"
)

// chunkInfo is what the gateway needs from one SSE data payload.
type chunkInfo struct {
	usage        *provider.Usage
	usageOnly    bool // the chunk carries usage and no choices: the synthetic final chunk
	contentBytes int  // generated text in this chunk, for the token estimate
}

func inspectChunk(payload []byte) chunkInfo {
	var c struct {
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				Reasoning        string `json:"reasoning"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
		Usage *provider.Usage `json:"usage"`
	}
	if json.Unmarshal(payload, &c) != nil {
		return chunkInfo{}
	}
	var info chunkInfo
	for _, ch := range c.Choices {
		info.contentBytes += len(ch.Delta.Content) + len(ch.Delta.Reasoning) + len(ch.Delta.ReasoningContent)
	}
	if c.Usage != nil && c.Usage.Total() > 0 {
		info.usage = c.Usage
		info.usageOnly = len(c.Choices) == 0
	}
	return info
}

// stripUsage removes the top-level "usage" field from a chunk, leaving the rest verbatim.
func stripUsage(payload []byte) ([]byte, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(payload, &m) != nil {
		return nil, false
	}
	if _, ok := m["usage"]; !ok {
		return nil, false
	}
	delete(m, "usage")
	out, err := json.Marshal(m)
	return out, err == nil
}

func clientWantsUsage(req map[string]json.RawMessage) bool {
	var so struct {
		IncludeUsage bool `json:"include_usage"`
	}
	raw, ok := req["stream_options"]
	return ok && json.Unmarshal(raw, &so) == nil && so.IncludeUsage
}

// ceilDiv4 is the gateway's token estimate: about four bytes per token.
func ceilDiv4(n int) int64 { return int64((n + 3) / 4) }

// serveStream proxies a server-sent-events completion straight through.
//
// Failover can only happen before the first byte reaches the client (the attempt
// helper handles that). After that, an upstream failure ends the stream: the
// connection is aborted so the client sees a truncated response rather than a
// clean end. Streams are never cached.
func (s *Server) serveStream(w http.ResponseWriter, r *http.Request, app *auth.App, targets []config.Target,
	req map[string]json.RawMessage, st *reqState) {
	ctx := r.Context()
	upstream, used, attempts, f := attempt(s, ctx, targets, func(p *provider.Provider, m string) (*provider.Stream, error) {
		return p.ChatStream(ctx, m, req)
	})
	st.ev.Attempts = attempts
	if upstream == nil {
		s.writeFailure(w, targets, attempts, f)
		return
	}
	defer func() { _ = upstream.Close() }()
	st.answeredBy = used.Provider
	st.ev.Provider, st.ev.Model = used.Provider, used.Model

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // tell nginx-style proxies not to buffer
	h.Set("X-Freeloader-Provider", used.Provider)
	h.Set("X-Freeloader-Cache", "BYPASS")
	h.Set("X-Freeloader-Attempts", strconv.Itoa(attempts))
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	// A provider that goes silent mid-stream must not hold the connection forever.
	var idleFired atomic.Bool
	idle := time.AfterFunc(s.StreamIdleTimeout, func() { idleFired.Store(true); _ = upstream.Close() })
	defer idle.Stop()

	wantUsage := clientWantsUsage(req)
	var (
		usage        *provider.Usage
		contentBytes int
		sawDone      bool
		skipBlank    bool
		clientGone   bool
		readErr      error
	)
	rd := bufio.NewReaderSize(upstream.Body, 32<<10)
	for {
		line, err := rd.ReadBytes('\n')
		if len(line) > 0 {
			idle.Reset(s.StreamIdleTimeout)
			forward := true
			trimmed := bytes.TrimRight(line, "\r\n")
			switch {
			case bytes.HasPrefix(trimmed, []byte("data:")):
				payload := bytes.TrimSpace(trimmed[len("data:"):])
				if string(payload) == "[DONE]" {
					sawDone = true
				} else {
					c := inspectChunk(payload)
					contentBytes += c.contentBytes
					if c.usage != nil {
						usage = c.usage
					}
					if c.usage != nil && !wantUsage {
						// We asked the provider for usage; the client didn't ask for it.
						if c.usageOnly {
							forward, skipBlank = false, true // the synthetic final chunk: drop it
						} else if stripped, ok := stripUsage(payload); ok {
							// Real providers put usage inside the last content chunk: remove just that field.
							line = append(append([]byte("data: "), stripped...), line[len(trimmed):]...)
						}
					}
				}
			case len(trimmed) == 0 && skipBlank:
				forward, skipBlank = false, false
			}
			if forward {
				if _, werr := w.Write(line); werr != nil {
					clientGone = true
					break
				}
				if len(trimmed) == 0 && flusher != nil { // blank line = end of an SSE event
					flusher.Flush()
				}
			}
		}
		if err != nil {
			readErr = err
			break
		}
	}

	// Tokens: the provider's final usage chunk when it sent one, otherwise an estimate.
	ev := st.ev
	if usage != nil {
		ev.PromptTokens, ev.CompletionTokens = usage.PromptTokens, usage.CompletionTokens
		if ev.CompletionTokens == 0 && usage.TotalTokens > usage.PromptTokens {
			ev.CompletionTokens = usage.TotalTokens - usage.PromptTokens
		}
	} else {
		ev.PromptTokens = ceilDiv4(len(req["messages"]))
		ev.CompletionTokens = ceilDiv4(contentBytes)
		ev.TokensEstimated = true
	}
	acct := context.WithoutCancel(ctx) // the client may be gone; still count what the provider spent
	if err := s.Limiter.AddTokens(acct, app, ev.PromptTokens+ev.CompletionTokens); err != nil {
		s.cacheFailed("add_tokens", err)
	}

	switch {
	case clientGone || ctx.Err() != nil:
		st.statusOverride = 499 // the client hung up
	case sawDone && (readErr == nil || errors.Is(readErr, io.EOF)):
		// completed normally
	default:
		// The upstream ended without [DONE] (broke, or went silent). We can't change the
		// status any more, so abort the connection: the client sees an incomplete response.
		s.Log.Warn("upstream stream ended early", "provider", used.Provider, "err", readErr, "idle_timeout", idleFired.Load())
		st.statusOverride = http.StatusBadGateway
		panic(http.ErrAbortHandler)
	}
}
