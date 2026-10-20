// Package server is the gateway's HTTP front door: auth, rate limit, cache,
// route with failover, record.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/aarshitaacharya/freeloader/gateway/internal/auth"
	"github.com/aarshitaacharya/freeloader/gateway/internal/cache"
	"github.com/aarshitaacharya/freeloader/gateway/internal/config"
	"github.com/aarshitaacharya/freeloader/gateway/internal/events"
	"github.com/aarshitaacharya/freeloader/gateway/internal/metrics"
	"github.com/aarshitaacharya/freeloader/gateway/internal/provider"
	"github.com/aarshitaacharya/freeloader/gateway/internal/ratelimit"
	"github.com/aarshitaacharya/freeloader/gateway/internal/route"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const maxRequestBytes = 4 << 20

type Deps struct {
	Auth        *auth.Authenticator
	Limiter     *ratelimit.Limiter
	KV          cache.KV
	Router      *route.Router
	Metrics     *metrics.Metrics
	Log         *slog.Logger
	MaxAttempts int
	Now         func() time.Time

	Admin http.Handler // serves /admin/*; nil = not mounted (404)

	Events            events.Publisher // nil = events disabled
	StreamIdleTimeout time.Duration    // max silence between chunks of a stream; default 60 s
}

type Server struct {
	Deps
	mux *http.ServeMux
}

func New(d Deps) *Server {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.MaxAttempts <= 0 {
		d.MaxAttempts = 3
	}
	if d.Events == nil {
		d.Events = events.Noop{}
	}
	if d.StreamIdleTimeout <= 0 {
		d.StreamIdleTimeout = 60 * time.Second
	}
	s := &Server{Deps: d, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	s.mux.HandleFunc("GET /v1/models", s.handleModels)
	if d.Admin != nil {
		s.mux.Handle("/admin/", d.Admin)
	}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	s.mux.HandleFunc("GET /readyz", s.handleReady)
	s.mux.Handle("GET /metrics", promhttp.HandlerFor(d.Metrics.Reg, promhttp.HandlerOpts{}))
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

// cachedResponse is what we store in fl-cache for a completed request.
type cachedResponse struct {
	Provider string          `json:"provider"`
	Model    string          `json:"model,omitempty"`
	Body     json.RawMessage `json:"body"`
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func writeError(w http.ResponseWriter, status int, typ, code, msg string, retryAfter time.Duration) {
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": typ, "param": nil, "code": code},
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Auth.Authenticate(r.Context(), r.Header.Get("Authorization")); err != nil {
		s.authError(w, err)
		return
	}
	data := []map[string]any{}
	for _, a := range s.Router.Aliases() {
		data = append(data, map[string]any{"id": a, "object": "model", "owned_by": "freeloader"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	for name, ping := range map[string]func(context.Context) error{"postgres": s.Auth.Ping, "fl-cache": s.KV.Ping} {
		if err := ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready: " + name + "\n"))
			return
		}
	}
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) authError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrMissingKey), errors.Is(err, auth.ErrInvalidKey):
		writeError(w, http.StatusUnauthorized, "invalid_request_error", "invalid_api_key", err.Error(), 0)
	case errors.Is(err, auth.ErrDisabled):
		writeError(w, http.StatusForbidden, "invalid_request_error", "app_disabled", err.Error(), 0)
	default:
		s.Log.Error("auth lookup failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, "api_error", "auth_unavailable", "authentication backend unavailable", 5*time.Second)
	}
}

// cacheFailed records a cache outage. The request carries on without the cache:
// the gateway's job is to keep serving.
func (s *Server) cacheFailed(op string, err error) {
	s.Metrics.CacheErrors.Inc()
	s.Log.Warn("cache unavailable; continuing without it", "op", op, "err", err)
}

func isZeroTemperature(req map[string]json.RawMessage) bool {
	raw, ok := req["temperature"]
	if !ok {
		return false // absent means the provider's default, which is not 0
	}
	var t float64
	return json.Unmarshal(raw, &t) == nil && t == 0
}

// reqState is what the deferred bookkeeping in handleChat needs to know.
type reqState struct {
	appName, alias, answeredBy string
	// ev is non-nil once the request is attributable to an app and well-formed.
	// Every such request, whatever its outcome, emits exactly one event.
	ev             *events.Event
	statusOverride int // set when the real outcome differs from what the client saw (aborted streams)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	start := s.Now()
	id := uuid.Must(uuid.NewV7()).String()
	w.Header().Set("X-Request-Id", id)
	sw := &statusWriter{ResponseWriter: w}
	w = sw
	st := &reqState{appName: "unknown", alias: "none"}
	defer func() { // runs on every exit path, including an aborted stream
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		if st.statusOverride != 0 {
			status = st.statusOverride
		}
		elapsed := s.Now().Sub(start)
		s.Metrics.Requests.WithLabelValues(st.appName, st.alias, strconv.Itoa(status)).Inc()
		if st.answeredBy != "" {
			s.Metrics.Latency.WithLabelValues(st.answeredBy).Observe(elapsed.Seconds())
		}
		s.Log.Info("request", "request_id", id, "app", st.appName, "alias", st.alias, "provider", st.answeredBy,
			"status", status, "ms", elapsed.Milliseconds())
		if st.ev != nil {
			st.ev.Status = status
			st.ev.LatencyMS = elapsed.Milliseconds()
			s.Events.Publish(*st.ev)
		}
	}()
	ctx := r.Context()

	// 1. Auth
	app, err := s.Auth.Authenticate(ctx, r.Header.Get("Authorization"))
	if err != nil {
		s.authError(w, err)
		return
	}
	st.appName = app.Name

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	var req map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json", "request body must be a JSON object", 0)
		return
	}
	var model string
	if raw, ok := req["model"]; !ok || json.Unmarshal(raw, &model) != nil || model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "missing_model", "\"model\" is required", 0)
		return
	}
	if len(req["messages"]) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "missing_messages", "\"messages\" is required", 0)
		return
	}
	stream := false
	if raw, ok := req["stream"]; ok {
		_ = json.Unmarshal(raw, &stream)
	}

	var targets []config.Target
	st.alias, targets, err = s.Router.Resolve(app, model)
	if err != nil {
		st.alias = "none"
		writeError(w, http.StatusNotFound, "invalid_request_error", "model_not_found",
			"unknown model \""+model+"\"; see GET /v1/models", 0)
		return
	}
	st.ev = &events.Event{RequestID: id, TS: start.UTC(), AppID: app.ID, Alias: st.alias}

	// 2. Rate limit (fails open if the cache is down)
	dec, err := s.Limiter.Check(ctx, app)
	if err != nil {
		s.cacheFailed("ratelimit", err)
	} else if !dec.Allowed {
		s.Metrics.RateLimited.WithLabelValues(app.Name).Inc()
		writeError(w, http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded",
			"app limit exceeded: "+string(dec.Reason), dec.RetryAfter)
		return
	}

	// Streams are proxied straight through and never cached.
	if stream {
		s.serveStream(w, r, app, targets, req, st)
		return
	}

	// 3. Cache lookup
	cacheable := app.CacheTTL > 0 && (isZeroTemperature(req) || app.CacheAll)
	var key string
	if cacheable {
		if key, err = CacheKey(model, req); err != nil {
			cacheable = false
		}
	}
	if cacheable {
		b, hit, err := s.KV.Get(ctx, key)
		if err != nil {
			s.cacheFailed("get", err)
		} else if hit {
			var c cachedResponse
			if json.Unmarshal(b, &c) == nil {
				s.Metrics.CacheHits.Inc()
				st.answeredBy = "cache"
				st.ev.CacheHit, st.ev.Provider, st.ev.Model = true, c.Provider, c.Model
				w.Header().Set("X-Freeloader-Cache", "HIT")
				w.Header().Set("X-Freeloader-Provider", c.Provider)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(c.Body)
				return
			}
		}
	}

	// 4+5. Route, call, fail over
	resp, used, attempts, failure := attempt(s, ctx, targets, func(p *provider.Provider, m string) (*provider.Response, error) {
		return p.Chat(ctx, m, req)
	})
	st.ev.Attempts = attempts
	if resp == nil {
		s.writeFailure(w, targets, attempts, failure)
		return
	}
	st.answeredBy = used.Provider
	st.ev.Provider, st.ev.Model = used.Provider, used.Model
	st.ev.PromptTokens = resp.Usage.PromptTokens
	st.ev.CompletionTokens = resp.Usage.CompletionTokens
	if st.ev.CompletionTokens == 0 && resp.Usage.TotalTokens > resp.Usage.PromptTokens {
		st.ev.CompletionTokens = resp.Usage.TotalTokens - resp.Usage.PromptTokens
	}

	// 6. Record, before responding so a follow-up request always sees the cache entry.
	if cacheable {
		env, _ := json.Marshal(cachedResponse{Provider: used.Provider, Model: used.Model, Body: resp.Body})
		if err := s.KV.Set(ctx, key, env, app.CacheTTL); err != nil {
			s.cacheFailed("set", err)
		}
	}
	if err := s.Limiter.AddTokens(ctx, app, resp.Usage.Total()); err != nil {
		s.cacheFailed("add_tokens", err)
	}

	w.Header().Set("X-Freeloader-Provider", used.Provider)
	w.Header().Set("X-Freeloader-Attempts", strconv.Itoa(attempts))
	if cacheable {
		w.Header().Set("X-Freeloader-Cache", "MISS")
	} else {
		w.Header().Set("X-Freeloader-Cache", "BYPASS")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp.Body)
}
