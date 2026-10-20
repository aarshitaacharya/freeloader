// Package provider calls an OpenAI-compatible chat completions endpoint and
// classifies failures into "try the next provider" vs "tell the client".
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"time"
)

const (
	maxResponseBytes = 16 << 20
	maxErrorBody     = 4 << 10
)

type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// Total is the provider's total if reported, otherwise prompt + completion.
func (u Usage) Total() int64 {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.PromptTokens + u.CompletionTokens
}

type Response struct {
	Body  []byte
	Usage Usage
}

// Error is any non-success outcome of an upstream call.
type Error struct {
	Provider string
	Status   int    // upstream HTTP status; 0 for network errors and timeouts
	Body     []byte // upstream error body (truncated), for passing client errors through
	// Failover is true when the problem is the provider's or ours rather than the
	// client's request, so another provider might succeed.
	Failover bool
	Err      error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("provider %s: %v", e.Provider, e.Err)
	}
	return fmt.Sprintf("provider %s: HTTP %d", e.Provider, e.Status)
}
func (e *Error) Unwrap() error { return e.Err }

// failoverStatus: 429 and 5xx per the spec, plus 401/403 (a revoked or wrong key
// is ours to fix, not the client's), 404 (model retired) and 408.
func failoverStatus(code int) bool {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return code >= 500
}

type Provider struct {
	Name    string
	BaseURL string
	APIKey  string
	Timeout time.Duration
	HTTP    *http.Client
}

func New(name, baseURL, apiKey string, timeout time.Duration) *Provider {
	return &Provider{
		Name: name, BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Timeout: timeout,
		HTTP: &http.Client{Transport: &http.Transport{
			MaxIdleConns: 100, MaxIdleConnsPerHost: 32, IdleConnTimeout: 90 * time.Second,
			ForceAttemptHTTP2: true,
		}},
	}
}

// Enabled reports whether the provider has an API key and can be routed to.
func (p *Provider) Enabled() bool { return p.APIKey != "" }

// Chat sends req (the client's request body) with "model" replaced by model.
func (p *Provider) Chat(ctx context.Context, model string, req map[string]json.RawMessage) (*Response, error) {
	body := maps.Clone(req)
	m, _ := json.Marshal(model)
	body["model"] = m
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, &Error{Provider: p.Name, Err: err}
	}

	cctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(cctx, http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, &Error{Provider: p.Name, Err: err}
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := p.HTTP.Do(hreq)
	if err != nil {
		// Network error or attempt timeout. If the *client* went away there is no
		// point trying elsewhere.
		return nil, &Error{Provider: p.Name, Failover: ctx.Err() == nil, Err: err}
	}
	defer resp.Body.Close()

	limit := int64(maxResponseBytes)
	if resp.StatusCode != http.StatusOK {
		limit = maxErrorBody
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, &Error{Provider: p.Name, Status: resp.StatusCode, Failover: ctx.Err() == nil, Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{Provider: p.Name, Status: resp.StatusCode, Body: b, Failover: failoverStatus(resp.StatusCode)}
	}

	var parsed struct {
		Usage Usage `json:"usage"`
	}
	_ = json.Unmarshal(b, &parsed) // a body we can't parse still goes back to the client
	return &Response{Body: b, Usage: parsed.Usage}, nil
}
