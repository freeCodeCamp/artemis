package worker

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	QueueDepth       *prometheus.GaugeVec
	DLQDepth         prometheus.Gauge
	WorkflowRuns     *prometheus.CounterVec
	WorkflowFailures *prometheus.CounterVec
	DeadLettered     *prometheus.CounterVec
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		QueueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "artemis_worker_queue_depth",
			Help: "Pending tasks per workflow queue (sampled from the engine).",
		}, []string{"workflow"}),
		DLQDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "artemis_worker_dlq_depth",
			Help: "Number of dead-lettered workflow runs awaiting operator attention.",
		}),
		WorkflowRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "artemis_worker_workflow_runs_total",
			Help: "Workflow runs, labelled by workflow and outcome.",
		}, []string{"workflow", "outcome"}),
		WorkflowFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "artemis_worker_workflow_failures_total",
			Help: "Workflow run failures, labelled by workflow.",
		}, []string{"workflow"}),
		DeadLettered: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "artemis_worker_dead_lettered_total",
			Help: "Workflow runs that exhausted retries and dead-lettered, labelled by workflow.",
		}, []string{"workflow"}),
	}
	reg.MustRegister(m.QueueDepth, m.DLQDepth, m.WorkflowRuns, m.WorkflowFailures, m.DeadLettered)
	return m
}

func (m *Metrics) ObserveRun(workflow, outcome string) {
	if m == nil {
		return
	}
	m.WorkflowRuns.WithLabelValues(workflow, outcome).Inc()
	if outcome == "failed" {
		m.WorkflowFailures.WithLabelValues(workflow).Inc()
	}
}

func (m *Metrics) ObserveDeadLetter(workflow string) {
	if m == nil {
		return
	}
	m.DeadLettered.WithLabelValues(workflow).Inc()
	m.DLQDepth.Inc()
}

func (m *Metrics) SetQueueDepth(workflow string, depth float64) {
	if m == nil {
		return
	}
	m.QueueDepth.WithLabelValues(workflow).Set(depth)
}

func (m *Metrics) SetDLQDepth(depth float64) {
	if m == nil {
		return
	}
	m.DLQDepth.Set(depth)
}
