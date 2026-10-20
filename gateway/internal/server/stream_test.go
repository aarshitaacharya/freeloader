package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aarshitaacharya/freeloader/gateway/internal/cache"
	"github.com/aarshitaacharya/freeloader/gateway/internal/events"
	"github.com/google/uuid"
)

func content(s string) string {
	return fmt.Sprintf(`{"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":%q},"finish_reason":null}]}`, s)
}

const (
	finishChunk = `{"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	usageChunk  = `{"id":"c","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
)

// sseUpstream describes a fake streaming provider.
type sseUpstream struct {
	chunks   []string
	gap      time.Duration
	hold     chan struct{} // if set, wait for it to close after the first chunk
	abortAt  int           // if > 0, kill the connection after this many chunks (no [DONE])
	stallAt  int           // if > 0, go silent after this many chunks
	noDone   bool
	gotBody  atomic.Value // map[string]any of the last request body
	canceled atomic.Bool  // set when the gateway cancelled the upstream request
}

func (u *sseUpstream) handler() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		u.gotBody.Store(body)
		w.Header().Set("Content-Type", "text/event-stream")
		rc := http.NewResponseController(w)
		for i, c := range u.chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", c)
			_ = rc.Flush()
			if i == 0 && u.hold != nil {
				select {
				case <-u.hold:
				case <-r.Context().Done():
					u.canceled.Store(true)
					return
				}
			}
			if u.abortAt > 0 && i+1 == u.abortAt {
				panic(http.ErrAbortHandler)
			}
			if u.stallAt > 0 && i+1 == u.stallAt {
				<-r.Context().Done()
				u.canceled.Store(true)
				return
			}
			select {
			case <-time.After(u.gap):
			case <-r.Context().Done():
				u.canceled.Store(true)
				return
			}
		}
		if !u.noDone {
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			_ = rc.Flush()
		}
	}
}

func standardChunks(withUsage bool) []string {
	c := []string{content("Hel"), content("lo "), content("wor"), content("ld"), finishChunk}
	if withUsage {
		c = append(c, usageChunk)
	}
	return c
}

const streamReq = `{"model":"fast","stream":true,"messages":[{"role":"user","content":"hi"}]}`
const streamReqUsage = `{"model":"fast","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`

func openStream(t *testing.T, h *harness, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", h.srv.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// dataLines returns the payload of every "data:" line.
func dataLines(t *testing.T, r io.Reader) ([]string, error) {
	t.Helper()
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if p, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			out = append(out, p)
		}
	}
	return out, sc.Err()
}

func lastEvent(t *testing.T, h *harness) events.Event {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if evs := h.events.Events(); len(evs) > 0 {
			return evs[len(evs)-1]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no event was published")
	return events.Event{}
}

func tokenCounter(t *testing.T, h *harness, appID int64) int64 {
	t.Helper()
	n, _ := cacheGetInt(h, fmt.Sprintf("tok:%d:%s", appID, time.Now().UTC().Format("2006-01-02")))
	return n
}

func TestStreamIsProxiedIncrementallyWithoutBuffering(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	up := &sseUpstream{chunks: standardChunks(true), gap: time.Millisecond, hold: make(chan struct{})}
	h.ups["a"].set(up.handler())

	resp := openStream(t, h, streamReqUsage)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status=%d type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("X-Freeloader-Provider") != "a" || resp.Header.Get("X-Freeloader-Cache") != "BYPASS" {
		t.Errorf("headers: provider=%q cache=%q", resp.Header.Get("X-Freeloader-Provider"), resp.Header.Get("X-Freeloader-Cache"))
	}

	// The upstream is still holding after its first chunk. If the gateway buffered the
	// response, this read would block until the upstream finished and the test would time out.
	br := bufio.NewReader(resp.Body)
	first := make(chan string, 1)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				first <- "ERR " + err.Error()
				return
			}
			if strings.HasPrefix(line, "data: ") {
				first <- strings.TrimSpace(strings.TrimPrefix(line, "data: "))
				return
			}
		}
	}()
	select {
	case got := <-first:
		if got != content("Hel") {
			t.Fatalf("first chunk = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first chunk did not arrive while the upstream was still streaming: the gateway is buffering")
	}
	close(up.hold)

	rest, err := dataLines(t, br)
	if err != nil {
		t.Fatal(err)
	}
	want := append(standardChunks(true)[1:], "[DONE]")
	if strings.Join(rest, "\n") != strings.Join(want, "\n") {
		t.Errorf("stream differs from upstream's:\n got %v\nwant %v", rest, want)
	}
}

func TestStreamUsageChunkVisibleOnlyIfClientAsked(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		wantUsage bool
	}{
		{"client did not ask: synthetic usage chunk is stripped", streamReq, false},
		{"client asked: usage chunk is forwarded", streamReqUsage, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, defaultApp(), 3)
			up := &sseUpstream{chunks: standardChunks(true)}
			h.ups["a"].set(up.handler())

			lines, err := dataLines(t, openStream(t, h, tc.body).Body)
			if err != nil {
				t.Fatal(err)
			}
			sawUsage := false
			for _, l := range lines {
				if strings.Contains(l, `"usage"`) {
					sawUsage = true
				}
			}
			if sawUsage != tc.wantUsage {
				t.Errorf("client saw usage chunk = %v, want %v", sawUsage, tc.wantUsage)
			}
			if lines[len(lines)-1] != "[DONE]" {
				t.Errorf("stream must end with [DONE], got %q", lines[len(lines)-1])
			}
			// Either way the gateway asked the provider for usage so it can count tokens.
			body := up.gotBody.Load().(map[string]any)
			opts, _ := body["stream_options"].(map[string]any)
			if body["stream"] != true || opts["include_usage"] != true || body["model"] != "model-a" {
				t.Errorf("upstream request = %v", body)
			}
		})
	}
}

func TestStreamTokensComeFromTheUsageChunk(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	h.ups["a"].set((&sseUpstream{chunks: standardChunks(true)}).handler())
	_, _ = dataLines(t, openStream(t, h, streamReq).Body)

	ev := lastEvent(t, h)
	if ev.PromptTokens != 10 || ev.CompletionTokens != 5 || ev.TokensEstimated {
		t.Errorf("event tokens = %d/%d estimated=%v, want exactly 10/5 not estimated", ev.PromptTokens, ev.CompletionTokens, ev.TokensEstimated)
	}
	if got := tokenCounter(t, h, ev.AppID); got != 15 {
		t.Errorf("token budget counter = %d, want 15", got)
	}
}

func TestStreamWithoutUsageIsEstimatedAndFlagged(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	h.ups["a"].set((&sseUpstream{chunks: standardChunks(false)}).handler()) // never sends usage
	lines, _ := dataLines(t, openStream(t, h, streamReq).Body)
	if len(lines) == 0 {
		t.Fatal("no stream")
	}
	ev := lastEvent(t, h)
	if !ev.TokensEstimated {
		t.Error("tokens_estimated must be true when the provider sent no usage")
	}
	// "Hello world" = 11 bytes -> ceil(11/4) = 3 completion tokens; the prompt estimate is nonzero.
	if ev.CompletionTokens != 3 || ev.PromptTokens == 0 {
		t.Errorf("estimate = %d prompt / %d completion", ev.PromptTokens, ev.CompletionTokens)
	}
	if got := tokenCounter(t, h, ev.AppID); got != ev.PromptTokens+ev.CompletionTokens {
		t.Errorf("budget counter = %d, want the estimate %d", got, ev.PromptTokens+ev.CompletionTokens)
	}
}

func TestStreamsAreNeverCached(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	up := &sseUpstream{chunks: standardChunks(true)}
	h.ups["a"].set(up.handler())
	body := `{"model":"fast","stream":true,"temperature":0,"messages":[{"role":"user","content":"hi"}]}`
	for i := 0; i < 2; i++ {
		_, _ = dataLines(t, openStream(t, h, body).Body)
	}
	if a, _, _ := h.calls(); a != 2 {
		t.Errorf("upstream calls = %d, want 2: streams must never be answered from cache", a)
	}
	if h.kv.Sets != 0 {
		t.Errorf("streams wrote %d cache entries, want 0", h.kv.Sets)
	}
}

func TestStreamFailsOverBeforeTheFirstByte(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	h.ups["a"].set(replyStatus(429))
	h.ups["b"].set((&sseUpstream{chunks: standardChunks(true)}).handler())
	resp := openStream(t, h, streamReq)
	lines, err := dataLines(t, resp.Body)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("status=%d err=%v", resp.StatusCode, err)
	}
	if resp.Header.Get("X-Freeloader-Provider") != "b" || resp.Header.Get("X-Freeloader-Attempts") != "2" {
		t.Errorf("provider=%q attempts=%q", resp.Header.Get("X-Freeloader-Provider"), resp.Header.Get("X-Freeloader-Attempts"))
	}
	if lines[len(lines)-1] != "[DONE]" {
		t.Error("stream incomplete")
	}
}

func TestStreamHeaderTimeoutFailsOver(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	h.router.Provider("a").Timeout = 150 * time.Millisecond
	h.ups["a"].set(func(w http.ResponseWriter, r *http.Request) { // never sends headers
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	})
	h.ups["b"].set((&sseUpstream{chunks: standardChunks(true)}).handler())
	resp := openStream(t, h, streamReq)
	if resp.StatusCode != 200 || resp.Header.Get("X-Freeloader-Provider") != "b" {
		t.Fatalf("status=%d provider=%q", resp.StatusCode, resp.Header.Get("X-Freeloader-Provider"))
	}
	// And the long stream itself is NOT bound by the header timeout.
	h.ups["b"].set((&sseUpstream{chunks: standardChunks(true), gap: 120 * time.Millisecond}).handler())
	h.router.Provider("b").Timeout = 150 * time.Millisecond
	start := time.Now()
	lines, err := dataLines(t, openStream(t, h, streamReq).Body)
	if err != nil || lines[len(lines)-1] != "[DONE]" {
		t.Fatalf("a stream that outlasts the attempt timeout must complete: err=%v", err)
	}
	if time.Since(start) < 400*time.Millisecond {
		t.Error("test did not exercise a stream longer than the attempt timeout")
	}
}

func TestStreamUpstreamBreakingMidStreamAbortsTheClient(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	h.ups["a"].set((&sseUpstream{chunks: standardChunks(false), abortAt: 2}).handler())
	resp := openStream(t, h, streamReq)
	lines, err := dataLines(t, resp.Body)
	if err == nil {
		t.Error("the client must see an error (truncated response), not a clean end")
	}
	for _, l := range lines {
		if l == "[DONE]" {
			t.Error("the gateway must not fabricate [DONE] for a truncated stream")
		}
	}
	ev := lastEvent(t, h)
	if ev.Status != 502 || !ev.TokensEstimated {
		t.Errorf("event status=%d estimated=%v, want 502 / true", ev.Status, ev.TokensEstimated)
	}
}

func TestStreamIdleTimeoutCutsAStalledProvider(t *testing.T) {
	h := newHarnessWith(t, defaultApp(), 3, func(d *Deps) { d.StreamIdleTimeout = 200 * time.Millisecond })
	up := &sseUpstream{chunks: standardChunks(false), stallAt: 2}
	h.ups["a"].set(up.handler())
	start := time.Now()
	_, err := dataLines(t, openStream(t, h, streamReq).Body)
	if err == nil {
		t.Error("a stalled stream must end in an error for the client")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("took %v: idle timeout did not fire", took)
	}
	if ev := lastEvent(t, h); ev.Status != 502 {
		t.Errorf("event status = %d, want 502", ev.Status)
	}
}

func TestStreamClientDisconnectCancelsUpstream(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	up := &sseUpstream{chunks: standardChunks(true), gap: 50 * time.Millisecond, hold: make(chan struct{})}
	h.ups["a"].set(up.handler())
	resp := openStream(t, h, streamReq)
	br := bufio.NewReader(resp.Body)
	for { // read the first event, then hang up
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data: ") {
			break
		}
	}
	_ = resp.Body.Close()

	deadline := time.Now().Add(3 * time.Second)
	for !up.canceled.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !up.canceled.Load() {
		t.Error("the upstream request was not cancelled after the client disconnected: quota keeps burning")
	}
	if ev := lastEvent(t, h); ev.Status != 499 {
		t.Errorf("event status = %d, want 499 (client closed)", ev.Status)
	}
}

func TestStreamCountsAgainstRateLimitsAndBudget(t *testing.T) {
	app := defaultApp()
	app.RPMLimit = 1
	h := newHarness(t, app, 3)
	h.ups["a"].set((&sseUpstream{chunks: standardChunks(true)}).handler())
	_, _ = dataLines(t, openStream(t, h, streamReq).Body)
	resp := openStream(t, h, streamReq)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Errorf("second streamed request: status=%d retry-after=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

func TestStreamAllProvidersFailBeforeStreamingGivesNormalError(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	for _, u := range h.ups {
		u.set(replyStatus(500))
	}
	resp := openStream(t, h, streamReq)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 502 || !strings.Contains(string(b), "upstream_error") {
		t.Errorf("status=%d body=%s", resp.StatusCode, b)
	}
}

// ---- usage events ----------------------------------------------------------

func TestEveryRequestEmitsExactlyOneEvent(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	r := h.chat(det)
	wantStatus(t, r, 200)
	evs := h.events.Events()
	if len(evs) != 1 {
		t.Fatalf("got %d events for one request", len(evs))
	}
	e := evs[0]
	id, err := uuidParse(e.RequestID)
	if err != nil || id.Version() != 7 {
		t.Errorf("request_id %q is not a UUIDv7 (err=%v)", e.RequestID, err)
	}
	if r.header.Get("X-Request-Id") != e.RequestID {
		t.Errorf("X-Request-Id %q != event request_id %q", r.header.Get("X-Request-Id"), e.RequestID)
	}
	if e.AppID != 1 || e.Provider != "a" || e.Model != "model-a" || e.Alias != "fast" {
		t.Errorf("identity fields: %+v", e)
	}
	if e.PromptTokens != 10 || e.CompletionTokens != 5 || e.TokensEstimated {
		t.Errorf("tokens: %+v", e)
	}
	if e.Status != 200 || e.CacheHit || e.Attempts != 1 {
		t.Errorf("outcome fields: %+v", e)
	}
	if e.TS.IsZero() || time.Since(e.TS) > time.Minute || e.LatencyMS < 0 {
		t.Errorf("ts=%v latency=%d", e.TS, e.LatencyMS)
	}
}

func TestEventForCacheHitUsesNoQuota(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	h.chat(det)
	h.chat(det)
	evs := h.events.Events()
	if len(evs) != 2 {
		t.Fatalf("events = %d", len(evs))
	}
	hit := evs[1]
	if !hit.CacheHit || hit.Provider != "a" || hit.Model != "model-a" || hit.Attempts != 0 || hit.PromptTokens != 0 || hit.CompletionTokens != 0 {
		t.Errorf("cache-hit event: %+v", hit)
	}
	if evs[0].RequestID == hit.RequestID {
		t.Error("request ids must be unique")
	}
}

func TestEventOutcomes(t *testing.T) {
	t.Run("failover records the answering provider and attempts", func(t *testing.T) {
		h := newHarness(t, defaultApp(), 3)
		h.ups["a"].set(replyStatus(500))
		h.chat(nondet)
		if e := lastEvent(t, h); e.Provider != "b" || e.Attempts != 2 || e.Status != 200 {
			t.Errorf("%+v", e)
		}
	})
	t.Run("total failure", func(t *testing.T) {
		h := newHarness(t, defaultApp(), 3)
		for _, u := range h.ups {
			u.set(replyStatus(500))
		}
		h.chat(nondet)
		if e := lastEvent(t, h); e.Status != 502 || e.Provider != "" || e.Attempts != 3 || e.PromptTokens != 0 {
			t.Errorf("%+v", e)
		}
	})
	t.Run("rate limited", func(t *testing.T) {
		app := defaultApp()
		app.RPMLimit = 1
		h := newHarness(t, app, 3)
		h.chat(nondet)
		h.chat(nondet)
		evs := h.events.Events()
		if len(evs) != 2 || evs[1].Status != 429 || evs[1].Attempts != 0 {
			t.Errorf("%+v", evs)
		}
	})
	t.Run("requests that never reach routing emit nothing", func(t *testing.T) {
		h := newHarness(t, defaultApp(), 3)
		h.do("POST", "/v1/chat/completions", nondet, nil)                                                 // no auth
		h.chat(`{nope`)                                                                                   // invalid JSON
		h.chat(`{"model":"nope","messages":[{"role":"user","content":"x"}]}`)                             // unknown model
		h.do("POST", "/v1/chat/completions", nondet, map[string]string{"Authorization": "Bearer fl_bad"}) // bad key
		if n := len(h.events.Events()); n != 0 {
			t.Errorf("%d events for requests that were never served", n)
		}
	})
	t.Run("N requests produce N events", func(t *testing.T) {
		h := newHarness(t, defaultApp(), 3)
		for i := 0; i < 25; i++ {
			h.chat(nondet)
		}
		if n := len(h.events.Events()); n != 25 {
			t.Errorf("%d events, want 25", n)
		}
	})
}

func uuidParse(s string) (uuid.UUID, error) { return uuid.Parse(s) }

func cacheGetInt(h *harness, key string) (int64, error) {
	return cache.GetInt(context.Background(), h.kv, key)
}

// Real providers (Groq, Gemini, OpenRouter) put usage inside the final *content*
// chunk instead of sending a separate empty-choices chunk. A client that did not ask
// for usage must not receive it, but the gateway still counts the tokens.
func TestStreamStripsUsageEmbeddedInAContentChunkUnlessRequested(t *testing.T) {
	embedded := `{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	chunks := []string{content("Hel"), content("lo"), embedded}
	for _, tc := range []struct {
		name      string
		body      string
		wantUsage bool
	}{
		{"not requested: usage field removed from the chunk", streamReq, false},
		{"requested: chunk forwarded untouched", streamReqUsage, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, defaultApp(), 3)
			h.ups["a"].set((&sseUpstream{chunks: chunks}).handler())
			lines, err := dataLines(t, openStream(t, h, tc.body).Body)
			if err != nil {
				t.Fatal(err)
			}
			var finishSeen, usageSeen bool
			for _, l := range lines {
				if l == "[DONE]" {
					continue
				}
				var c map[string]json.RawMessage
				if err := json.Unmarshal([]byte(l), &c); err != nil {
					t.Fatalf("gateway produced invalid JSON %q: %v", l, err)
				}
				if _, ok := c["usage"]; ok {
					usageSeen = true
				}
				if strings.Contains(l, `"finish_reason":"stop"`) {
					finishSeen = true // the finish chunk itself must survive the rewrite
				}
			}
			if usageSeen != tc.wantUsage {
				t.Errorf("client saw usage = %v, want %v", usageSeen, tc.wantUsage)
			}
			if !finishSeen {
				t.Error("the finish_reason chunk was lost")
			}
			if ev := lastEvent(t, h); ev.PromptTokens != 10 || ev.CompletionTokens != 5 || ev.TokensEstimated {
				t.Errorf("tokens must still come from the provider's usage: %+v", ev)
			}
		})
	}
}
