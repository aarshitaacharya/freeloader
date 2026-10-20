// Package metrics defines the gateway's Prometheus metrics. Labels stay
// low-cardinality: never request ids or user text.
package metrics

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	Reg            *prometheus.Registry
	Requests       *prometheus.CounterVec   // app, alias, status
	Latency        *prometheus.HistogramVec // provider ("cache" for cache hits)
	CacheHits      prometheus.Counter
	Failovers      *prometheus.CounterVec // from, to
	RateLimited    *prometheus.CounterVec // app
	UpstreamErrors *prometheus.CounterVec // provider, code
	EventsDropped  prometheus.Counter     // wired up in level 5
	CacheErrors    prometheus.Counter     // cache unreachable; requests were served without it
}

func New() *Metrics {
	m := &Metrics{
		Reg: prometheus.NewRegistry(),
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "fl_gateway_requests_total", Help: "Requests handled, by app, alias and HTTP status."},
			[]string{"app", "alias", "status"}),
		Latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "fl_gateway_request_seconds", Help: "End-to-end request latency by answering provider (\"cache\" for cache hits).",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}},
			[]string{"provider"}),
		CacheHits: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "fl_gateway_cache_hits_total", Help: "Requests answered from the response cache."}),
		Failovers: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "fl_gateway_failovers_total", Help: "Times a request moved from one provider to the next."},
			[]string{"from", "to"}),
		RateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "fl_gateway_ratelimited_total", Help: "Requests rejected by a per-app limit."},
			[]string{"app"}),
		UpstreamErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "fl_gateway_upstream_errors_total", Help: "Failed upstream calls by provider and HTTP status (0 = network/timeout)."},
			[]string{"provider", "code"}),
		EventsDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "fl_gateway_events_dropped_total", Help: "Usage events dropped because the event stream was unavailable."}),
		CacheErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "fl_gateway_cache_errors_total", Help: "fl-cache calls that failed; the request was served without cache or limits."}),
	}
	m.Reg.MustRegister(m.Requests, m.Latency, m.CacheHits, m.Failovers, m.RateLimited,
		m.UpstreamErrors, m.EventsDropped, m.CacheErrors)
	return m
}
