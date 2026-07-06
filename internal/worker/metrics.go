package worker

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	QueueDepth       *prometheus.GaugeVec
	DLQDepth         prometheus.Gauge
	WorkflowRuns     *prometheus.CounterVec
	WorkflowFailures *prometheus.CounterVec
	WorkflowDuration *prometheus.HistogramVec
	DeadLettered     *prometheus.CounterVec
	RelayPublished   prometheus.Counter
	RelayFailures    prometheus.Counter
	RelayDuration    prometheus.Histogram
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
		WorkflowDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "artemis_worker_workflow_duration_seconds",
			Help:    "Workflow run wall-clock duration, labelled by workflow.",
			Buckets: prometheus.DefBuckets,
		}, []string{"workflow"}),
		DeadLettered: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "artemis_worker_dead_lettered_total",
			Help: "Workflow runs that exhausted retries and dead-lettered, labelled by workflow.",
		}, []string{"workflow"}),
		RelayPublished: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "artemis_relay_published_total",
			Help: "Outbox rows published to the engine by the relay loop (at-least-once).",
		}),
		RelayFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "artemis_relay_failures_total",
			Help: "Relay RunOnce passes that returned an error before draining the batch.",
		}),
		RelayDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "artemis_relay_runonce_duration_seconds",
			Help:    "Relay RunOnce pass wall-clock duration.",
			Buckets: prometheus.DefBuckets,
		}),
	}
	reg.MustRegister(m.QueueDepth, m.DLQDepth, m.WorkflowRuns, m.WorkflowFailures, m.WorkflowDuration, m.DeadLettered, m.RelayPublished, m.RelayFailures, m.RelayDuration)
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

func (m *Metrics) ObserveDuration(workflow string, seconds float64) {
	if m == nil {
		return
	}
	m.WorkflowDuration.WithLabelValues(workflow).Observe(seconds)
}

func (m *Metrics) ObserveRelayDuration(seconds float64) {
	if m == nil {
		return
	}
	m.RelayDuration.Observe(seconds)
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

func (m *Metrics) ObserveRelay(published int, err error) {
	if m == nil {
		return
	}
	if published > 0 {
		m.RelayPublished.Add(float64(published))
	}
	if err != nil {
		m.RelayFailures.Inc()
	}
}
