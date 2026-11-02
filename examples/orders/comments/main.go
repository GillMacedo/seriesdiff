// Command orders is a small made-up HTTP service used to produce real
// Prometheus scrapes for seriesdiff's examples. It isn't a real product.
package main

import (
	"flag"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	requests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP requests by method, route and status code.",
	}, []string{"method", "route", "code"})

	duration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency by route.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route"})

	paymentErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "payment_errors_total",
		Help: "Failed payment attempts by reason.",
	}, []string{"reason"})

	queueDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "order_queue_depth",
		Help: "Orders waiting to be processed.",
	})

	cacheEntries = promauto.NewCounter(prometheus.CounterOpts{
		Name: "cache_entries",
		Help: "Entries in the order cache.",
	})
)

// route maps a request path to its route template, so each order ID
// doesn't become its own series.
func route(path string) string {
	if strings.HasPrefix(path, "/orders/") {
		return "/orders/{id}"
	}
	return path
}

// handle serves the order API. Every request is counted and timed by
// route, never by raw path.
func handle(w http.ResponseWriter, r *http.Request) {
	began := time.Now()
	code := http.StatusOK
	switch {
	case r.URL.Path == "/orders" && r.Method == http.MethodPost:
		queueDepth.Inc()
		cacheEntries.Inc()
		code = http.StatusCreated
	case strings.HasPrefix(r.URL.Path, "/orders/"):
		if _, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/orders/")); err != nil {
			code = http.StatusNotFound
		}
	case r.URL.Path == "/pay":
		reason := r.URL.Query().Get("fail")
		if reason != "" {
			paymentErrors.WithLabelValues(reason).Inc()
			code = http.StatusPaymentRequired
		}
	default:
		code = http.StatusNotFound
	}
	w.WriteHeader(code)
	requests.WithLabelValues(r.Method, route(r.URL.Path), strconv.Itoa(code)).Inc()
	duration.WithLabelValues(route(r.URL.Path)).Observe(time.Since(began).Seconds())
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "listen address")
	flag.Parse()
	// The metrics endpoint is served next to the API on the same port.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/", handle)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
