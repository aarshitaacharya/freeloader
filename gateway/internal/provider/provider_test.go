package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func body(model string) map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"model":    json.RawMessage(`"ignored"`),
		"messages": json.RawMessage(`[{"role":"user","content":"hi"}]`),
	}
}

func TestChatSuccessRewritesModelAndParsesUsage(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, `{"id":"x","usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	}))
	defer srv.Close()

	p := New("p", srv.URL+"/v1/", "secret", time.Second)
	resp, err := p.Chat(context.Background(), "real-model", body("x"))
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/chat/completions" || gotAuth != "Bearer secret" {
		t.Errorf("path=%q auth=%q", gotPath, gotAuth)
	}
	if gotBody["model"] != "real-model" {
		t.Errorf("model sent = %v", gotBody["model"])
	}
	if resp.Usage.Total() != 7 || resp.Usage.PromptTokens != 3 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestChatDoesNotMutateCallersRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{}`) }))
	defer srv.Close()
	req := body("x")
	_, _ = New("p", srv.URL, "k", time.Second).Chat(context.Background(), "m2", req)
	if string(req["model"]) != `"ignored"` {
		t.Errorf("caller's map was modified: %s", req["model"])
	}
}

func TestUsageTotalFallsBackToSum(t *testing.T) {
	if (Usage{PromptTokens: 2, CompletionTokens: 5}).Total() != 7 {
		t.Error("expected prompt+completion when total is absent")
	}
}

func TestErrorClassification(t *testing.T) {
	tests := []struct {
		status   int
		failover bool
	}{
		{429, true}, {500, true}, {502, true}, {503, true},
		{401, true}, {403, true}, {404, true}, {408, true},
		{400, false}, {422, false}, {413, false},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"error":"nope"}`)
			}))
			defer srv.Close()
			_, err := New("p", srv.URL, "k", time.Second).Chat(context.Background(), "m", body("x"))
			perr, ok := err.(*Error)
			if !ok {
				t.Fatalf("err = %T %v", err, err)
			}
			if perr.Status != tc.status || perr.Failover != tc.failover {
				t.Errorf("got status=%d failover=%v, want %d/%v", perr.Status, perr.Failover, tc.status, tc.failover)
			}
			if string(perr.Body) != `{"error":"nope"}` {
				t.Errorf("body = %q", perr.Body)
			}
		})
	}
}

func TestTimeoutFailsOver(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	start := time.Now()
	_, err := New("p", srv.URL, "k", 100*time.Millisecond).Chat(context.Background(), "m", body("x"))
	perr, ok := err.(*Error)
	if !ok || !perr.Failover || perr.Status != 0 {
		t.Fatalf("err = %#v, want a failover Error with status 0", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("timeout not enforced")
	}
}

func TestConnectionRefusedFailsOver(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := New("p", url, "k", time.Second).Chat(context.Background(), "m", body("x"))
	if perr, ok := err.(*Error); !ok || !perr.Failover {
		t.Fatalf("err = %#v", err)
	}
}

func TestClientCancellationDoesNotFailOver(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // the server only notices a dropped client once the body is read
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	_, err := New("p", srv.URL, "k", 5*time.Second).Chat(ctx, "m", body("x"))
	if perr, ok := err.(*Error); !ok || perr.Failover {
		t.Fatalf("err = %#v, want non-failover", err)
	}
}

func TestEnabledRequiresKey(t *testing.T) {
	if New("p", "http://x", "", time.Second).Enabled() {
		t.Error("no key should mean disabled")
	}
	if !New("p", "http://x", "k", time.Second).Enabled() {
		t.Error("key should enable")
	}
}

func TestLargeErrorBodiesAreTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, strings.Repeat("x", 100000))
	}))
	defer srv.Close()
	_, err := New("p", srv.URL, "k", time.Second).Chat(context.Background(), "m", body("x"))
	if perr := err.(*Error); len(perr.Body) != maxErrorBody {
		t.Errorf("len(body) = %d, want %d", len(perr.Body), maxErrorBody)
	}
}
