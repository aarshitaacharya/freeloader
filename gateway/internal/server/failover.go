package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/aarshitaacharya/freeloader/gateway/internal/config"
	"github.com/aarshitaacharya/freeloader/gateway/internal/provider"
)

type failure struct {
	last        *provider.Error
	passthrough bool // last is the client's fault (e.g. 400): relay it instead of failing over
	canceled    bool // the client went away
}

// callWithFailover tries targets in order, at most MaxAttempts real calls.
// Providers that are cooling down, flagged exhausted, or have no key are skipped
// without costing an attempt.
func (s *Server) callWithFailover(ctx context.Context, targets []config.Target, req map[string]json.RawMessage) (*provider.Response, string, int, failure) {
	var (
		f        failure
		attempts int
		prev     string
	)
	for _, t := range targets {
		if attempts >= s.MaxAttempts {
			break
		}
		if reason, skip := s.Router.Skip(ctx, t.Provider); skip {
			s.Log.Debug("skipping provider", "provider", t.Provider, "reason", reason)
			continue
		}
		if prev != "" {
			s.Metrics.Failovers.WithLabelValues(prev, t.Provider).Inc()
		}
		attempts++

		resp, err := s.Router.Provider(t.Provider).Chat(ctx, t.Model, req)
		if err == nil {
			return resp, t.Provider, attempts, f
		}
		if ctx.Err() != nil {
			f.canceled = true
			return nil, "", attempts, f
		}
		var perr *provider.Error
		if !errors.As(err, &perr) {
			perr = &provider.Error{Provider: t.Provider, Failover: true, Err: err}
		}
		s.Metrics.UpstreamErrors.WithLabelValues(t.Provider, strconv.Itoa(perr.Status)).Inc()
		s.Log.Warn("upstream call failed", "provider", t.Provider, "model", t.Model,
			"status", perr.Status, "failover", perr.Failover, "err", perr.Err)
		f.last = perr
		if !perr.Failover {
			f.passthrough = true
			return nil, "", attempts, f
		}
		if perr.Status == http.StatusTooManyRequests {
			s.Router.StartCooldown(t.Provider)
		}
		prev = t.Provider
	}
	return nil, "", attempts, f
}

func (s *Server) writeFailure(w http.ResponseWriter, targets []config.Target, attempts int, f failure) {
	switch {
	case f.canceled:
		w.WriteHeader(499) // client closed request
	case f.passthrough:
		w.Header().Set("X-Freeloader-Provider", f.last.Provider)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.last.Status)
		if len(f.last.Body) > 0 {
			_, _ = w.Write(f.last.Body)
		}
	case attempts == 0:
		wait := s.Router.CooldownRemaining(targets)
		if wait <= 0 {
			wait = 5 * time.Second
		}
		writeError(w, http.StatusServiceUnavailable, "api_error", "all_providers_unavailable",
			"every provider for this model is cooling down, exhausted or not configured", wait)
	case f.last != nil && f.last.Status == http.StatusTooManyRequests:
		writeError(w, http.StatusTooManyRequests, "rate_limit_error", "providers_rate_limited",
			"every provider for this model is rate limited", s.Router.CooldownRemaining(targets))
	default:
		msg := "all providers failed"
		if f.last != nil {
			msg += " (last: " + f.last.Error() + ")"
		}
		writeError(w, http.StatusBadGateway, "api_error", "upstream_error", msg, 0)
	}
}
