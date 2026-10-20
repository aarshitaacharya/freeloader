package server

import (
	"context"
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

// attempt tries targets in order, at most MaxAttempts real calls, using call for
// each. It serves both buffered and streaming requests. Providers that are
// cooling down, flagged exhausted, or have no key are skipped without costing an
// attempt.
func attempt[T any](s *Server, ctx context.Context, targets []config.Target,
	call func(p *provider.Provider, model string) (T, error)) (res T, used config.Target, attempts int, f failure) {
	var prev string
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

		out, err := call(s.Router.Provider(t.Provider), t.Model)
		if err == nil {
			return out, t, attempts, f
		}
		if ctx.Err() != nil {
			f.canceled = true
			return res, used, attempts, f
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
			return res, used, attempts, f
		}
		if cooldownWorthy(perr) {
			s.Router.StartCooldown(t.Provider)
		}
		prev = t.Provider
	}
	return res, used, attempts, f
}

// cooldownWorthy: failures where trying the same provider again straight away is
// pointless, so this replica stops routing to it for the cooldown period. That is
// 429 (rate limited, per the spec), but also a rejected or unfunded key
// (401/402/403) and a timeout: a hung provider would otherwise cost every
// request a full attempt timeout. Plain 5xx are not included: they are often
// momentary, and failing fast on them is cheap.
func cooldownWorthy(e *provider.Error) bool {
	switch e.Status {
	case http.StatusTooManyRequests, http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden:
		return true
	}
	return e.Status == 0 && errors.Is(e.Err, context.DeadlineExceeded)
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
