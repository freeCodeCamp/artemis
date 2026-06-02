package gc

import "github.com/prometheus/client_golang/prometheus"

const (
	WorkflowGCSiteLabel         = "gc-site"
	WorkflowTombstonePurgeLabel = "tombstone-purge"
)

type Metrics struct {
	DeploysTombstoned prometheus.Counter
	BytesReclaimed    prometheus.Counter
	Runs              *prometheus.CounterVec
	Drift             *prometheus.CounterVec
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		DeploysTombstoned: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "artemis_gc_deploys_tombstoned_total",
			Help: "Count of deploys soft-deleted (moved to _trash) by retention GC, manual delete, and site purge.",
		}),
		BytesReclaimed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "artemis_gc_bytes_reclaimed_total",
			Help: "Bytes hard-reclaimed from _trash by the tombstone-purge pass past the recovery window.",
		}),
		Runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "artemis_gc_runs_total",
			Help: "Count of GC workflow runs, labelled by workflow and outcome.",
		}, []string{"workflow", "outcome"}),
		Drift: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "artemis_gc_drift_total",
			Help: "Reconcile drift events, labelled by kind (reindexed, orphan, pruned, aliased_missing).",
		}, []string{"kind"}),
	}
	reg.MustRegister(m.DeploysTombstoned, m.BytesReclaimed, m.Runs, m.Drift)
	return m
}

func (m *Metrics) drift(kind string, n int) {
	if m == nil || n == 0 {
		return
	}
	m.Drift.WithLabelValues(kind).Add(float64(n))
}

func (m *Metrics) tombstoned(n int) {
	if m == nil {
		return
	}
	m.DeploysTombstoned.Add(float64(n))
}

func (m *Metrics) reclaimed(bytes int64) {
	if m == nil {
		return
	}
	m.BytesReclaimed.Add(float64(bytes))
}

func (m *Metrics) run(workflow, outcome string) {
	if m == nil {
		return
	}
	m.Runs.WithLabelValues(workflow, outcome).Inc()
}
