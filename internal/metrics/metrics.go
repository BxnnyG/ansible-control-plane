// Package metrics holds the application's Prometheus collectors (namespace
// acp_). Collectors live in the default registry, which is the one served by
// /metrics. Label values come from small fixed sets only: never user input,
// hostnames, workspace names or raw URL paths.
package metrics

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const ns = "acp"

var (
	// RunsStarted counts runs claimed by a worker.
	RunsStarted = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: ns, Name: "runs_started_total", Help: "Runs claimed by a worker."})
	// RunsFinished counts runs reaching a final status (succeeded, failed, cancelled, lost).
	RunsFinished = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "runs_finished_total", Help: "Runs that reached a final status."}, []string{"status"})
	// RunDuration observes the execution time of finished runs.
	RunDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: ns, Name: "run_duration_seconds", Help: "Execution time of finished runs.",
		Buckets: []float64{5, 15, 30, 60, 120, 300, 600, 1800, 3600}}, []string{"status"})
	// WorkerClaims counts worker claim requests by outcome (claimed, empty, error).
	WorkerClaims = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "worker_claims_total", Help: "Worker claim requests by result."}, []string{"result"})
	// WorkerHeartbeats counts worker heartbeats by result (ok, lease_lost, error).
	WorkerHeartbeats = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "worker_heartbeats_total", Help: "Worker heartbeats by result."}, []string{"result"})

	// ConnectorSyncs counts connector sync runs by connector type and result (ok, error).
	ConnectorSyncs = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "connector_syncs_total", Help: "Connector syncs by type and result."}, []string{"type", "result"})
	// ConnectorLastSuccess is the unix time of the last successful sync per connector type.
	ConnectorLastSuccess = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Name: "connector_last_success_timestamp_seconds",
		Help: "Unix time of the last successful sync per connector type."}, []string{"type"})
	// ProjectSyncs counts Git project syncs by result (ok, error).
	ProjectSyncs = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "project_syncs_total", Help: "Git project syncs by result."}, []string{"result"})

	// WebhookRequests counts incoming webhook requests by kind (project, alert) and result.
	WebhookRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "webhook_requests_total", Help: "Incoming webhook requests by kind and result."},
		[]string{"kind", "result"})
	// LoginAttempts counts login attempts by method (oidc, breakglass) and result.
	LoginAttempts = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "login_attempts_total", Help: "Login attempts by method and result."},
		[]string{"method", "result"})
	// HTTPRequests counts HTTP requests by chi route pattern, method and status.
	HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "http_requests_total", Help: "HTTP requests by route pattern, method and status."},
		[]string{"route", "method", "status"})
	// HTTPDuration observes request latency by route pattern.
	HTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: ns, Name: "http_request_duration_seconds", Help: "HTTP request latency by route pattern.",
		Buckets: prometheus.DefBuckets}, []string{"route"})
)

// QueuedFunc returns the number of queued runs.
type QueuedFunc func(ctx context.Context) (int64, error)

var queuedSource atomic.Pointer[QueuedFunc]

func init() {
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: ns, Name: "runs_queued", Help: "Runs currently waiting for a worker (computed on scrape)."},
		func() float64 {
			f := queuedSource.Load()
			if f == nil {
				return 0
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			n, err := (*f)(ctx)
			if err != nil {
				return -1
			}
			return float64(n)
		})
}

// SetQueuedSource installs the query behind acp_runs_queued. A value of -1
// is exported when the query fails.
func SetQueuedSource(f QueuedFunc) { queuedSource.Store(&f) }

// Result maps an error to the label values "ok" / "error".
func Result(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}
