// Command openmetrics prints one real scrape from client_golang, with
// exemplars and _created lines, for seriesdiff's parser tests. It prints
// OpenMetrics by default, and the Prometheus text format with the argument
// "text". The exemplar and _created timestamps change on every run.
package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	reg := prometheus.NewRegistry()
	jobs := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "jobs_processed_total",
		Help: "Jobs processed by queue.",
	}, []string{"queue"})
	latency := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "job_duration_seconds",
		Help:    "Job duration.",
		Buckets: []float64{0.1, 1, 10},
	})
	summary := prometheus.NewSummary(prometheus.SummaryOpts{
		Name:       "job_payload_bytes",
		Help:       "Job payload size.",
		Objectives: map[float64]float64{0.5: 0.05, 0.99: 0.001},
	})
	inflight := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "jobs_in_flight",
		Help: "Jobs running now.",
	})
	reg.MustRegister(jobs, latency, summary, inflight)
	jobs.WithLabelValues("email").(prometheus.ExemplarAdder).AddWithExemplar(2, prometheus.Labels{"trace_id": "0af7651916cd43dd"})
	jobs.WithLabelValues("billing \"eu\"\\west").Add(1)
	latency.(prometheus.ExemplarObserver).ObserveWithExemplar(0.4, prometheus.Labels{"trace_id": "b7ad6b7169203331"})
	latency.Observe(3)
	summary.Observe(512)
	summary.Observe(2048)
	inflight.Set(2)

	h := promhttp.HandlerFor(reg, promhttp.HandlerOpts{EnableOpenMetrics: true, EnableOpenMetricsTextCreatedSamples: true})
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if len(os.Args) < 2 || os.Args[1] != "text" {
		req.Header.Set("Accept", "application/openmetrics-text; version=1.0.0; charset=utf-8")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	io.Copy(os.Stdout, rec.Body)
}
