// Package metrics holds fl-ledger's Prometheus metrics.
package metrics

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	Reg        *prometheus.Registry
	Events     prometheus.Counter   // events written to Postgres (new ones only)
	Duplicates prometheus.Counter   // redelivered events ignored: proof the dedupe fired
	Invalid    prometheus.Counter   // records that could never parse; skipped
	DBErrors   prometheus.Counter   // failed batch writes (retried)
	Lag        *prometheus.GaugeVec // partition: records behind the log end, for partitions this replica owns
	Batch      prometheus.Histogram // seconds per Postgres batch
}

func New() *Metrics {
	m := &Metrics{
		Reg: prometheus.NewRegistry(),
		Events: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "fl_ledger_events_total", Help: "Usage events newly written to Postgres."}),
		Duplicates: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "fl_ledger_duplicates_total", Help: "Redelivered events ignored because their request_id was already recorded."}),
		Invalid: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "fl_ledger_invalid_total", Help: "Records that were not a valid usage event and were skipped."}),
		DBErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "fl_ledger_db_errors_total", Help: "Failed Postgres batch writes (the batch is retried, offsets are not advanced)."}),
		Lag: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "fl_ledger_consumer_lag", Help: "Records between this replica's position and the end of the partition."},
			[]string{"partition"}),
		Batch: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "fl_ledger_batch_seconds", Help: "Time to write one batch to Postgres.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5}}),
	}
	m.Reg.MustRegister(m.Events, m.Duplicates, m.Invalid, m.DBErrors, m.Lag, m.Batch)
	return m
}
