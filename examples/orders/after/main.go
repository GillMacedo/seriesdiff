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
		Buckets: []float64{0.001, 0.01, 0.1, 1},
	}, []string{"route"})

	paymentErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "payment_errors_total",
		Help: "Failed payment attempts.",
	}, []string{})

	queueDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "orders_queue_depth",
		Help: "Orders waiting to be processed.",
	})

	cacheEntries = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "cache_entries",
		Help: "Entries in the order cache.",
	})
)

var orderItems = promauto.NewHistogram(prometheus.HistogramOpts{
	Name:    "order_items",
	Help:    "Items per created order.",
	Buckets: []float64{1, 5, 10},
})

func handle(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	code := http.StatusOK
	switch {
	case r.URL.Path == "/orders" && r.Method == http.MethodPost:
		queueDepth.Inc()
		cacheEntries.Inc()
		orderItems.Observe(3)
		code = http.StatusCreated
	case strings.HasPrefix(r.URL.Path, "/orders/"):
		if _, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/orders/")); err != nil {
			code = http.StatusNotFound
		}
	case r.URL.Path == "/pay":
		reason := r.URL.Query().Get("fail")
		if reason != "" {
			paymentErrors.WithLabelValues().Inc()
			code = http.StatusPaymentRequired
		}
	default:
		code = http.StatusNotFound
	}
	w.WriteHeader(code)
	requests.WithLabelValues(r.Method, r.URL.Path, strconv.Itoa(code)).Inc()
	duration.WithLabelValues(r.URL.Path).Observe(time.Since(start).Seconds())
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "listen address")
	flag.Parse()
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/", handle)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
