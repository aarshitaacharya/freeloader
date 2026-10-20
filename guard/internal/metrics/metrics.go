// Package metrics holds fl-guard's Prometheus metrics.
package metrics

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	Reg                                        *prometheus.Registry
	Events                                     prometheus.Counter // events that counted toward a provider's quota
	Duplicates                                 prometheus.Counter // redelivered events ignored via the seen: marker
	Skipped                                    prometheus.Counter // events that use no quota (cache hits, no provider) or are stale
	Invalid                                    prometheus.Counter
	Errors                                     prometheus.Counter   // cache failures while handling an event (retried)
	Tokens                                     *prometheus.GaugeVec // provider: tokens used today (provider-wide counter)
	Requests                                   *prometheus.GaugeVec // provider
	Exhausted                                  *prometheus.GaugeVec // provider: 1 while the provider-wide flag is set
	ModelTokens, ModelRequests, ModelExhausted *prometheus.GaugeVec // provider, model
}

func New() *Metrics {
	g := func(name, help string, labels ...string) *prometheus.GaugeVec {
		return prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
	}
	m := &Metrics{
		Reg:            prometheus.NewRegistry(),
		Events:         prometheus.NewCounter(prometheus.CounterOpts{Name: "fl_guard_events_total", Help: "Events counted toward provider quotas."}),
		Duplicates:     prometheus.NewCounter(prometheus.CounterOpts{Name: "fl_guard_duplicates_total", Help: "Redelivered events ignored."}),
		Skipped:        prometheus.NewCounter(prometheus.CounterOpts{Name: "fl_guard_skipped_total", Help: "Events that use no provider quota or belong to an earlier day."}),
		Invalid:        prometheus.NewCounter(prometheus.CounterOpts{Name: "fl_guard_invalid_total", Help: "Records that were not a valid usage event."}),
		Errors:         prometheus.NewCounter(prometheus.CounterOpts{Name: "fl_guard_cache_errors_total", Help: "fl-cache failures while handling events (retried)."}),
		Tokens:         g("fl_guard_provider_tokens_today", "Tokens the gateway has sent to a provider today (provider's own day).", "provider"),
		Requests:       g("fl_guard_provider_requests_today", "Requests the gateway has sent to a provider today.", "provider"),
		Exhausted:      g("fl_guard_exhausted", "1 while the provider is flagged exhausted (0 or 1).", "provider"),
		ModelTokens:    g("fl_guard_model_tokens_today", "Tokens sent to a provider's model today.", "provider", "model"),
		ModelRequests:  g("fl_guard_model_requests_today", "Requests sent to a provider's model today.", "provider", "model"),
		ModelExhausted: g("fl_guard_model_exhausted", "1 while the provider's model is flagged exhausted.", "provider", "model"),
	}
	m.Reg.MustRegister(m.Events, m.Duplicates, m.Skipped, m.Invalid, m.Errors, m.Tokens, m.Requests,
		m.Exhausted, m.ModelTokens, m.ModelRequests, m.ModelExhausted)
	return m
}
