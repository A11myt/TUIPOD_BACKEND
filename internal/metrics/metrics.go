package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tuipod_http_requests_total",
		Help: "Total HTTP requests by method, route, and status code.",
	}, []string{"method", "route", "status"})

	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "tuipod_http_request_duration_seconds",
		Help:    "HTTP request latency in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})
)

// RegisterDBStats exposes idle/total connection pool gauges.
func RegisterDBStats(pool *pgxpool.Pool) {
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "tuipod_db_connections_idle",
		Help: "Idle DB pool connections.",
	}, func() float64 { return float64(pool.Stat().IdleConns()) })

	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "tuipod_db_connections_total",
		Help: "Total DB pool connections.",
	}, func() float64 { return float64(pool.Stat().TotalConns()) })
}

// Handler returns the Prometheus scrape endpoint.
func Handler() http.Handler { return promhttp.Handler() }

// Middleware records request count and latency per chi route pattern.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		status := strconv.Itoa(rw.status)
		httpRequests.WithLabelValues(r.Method, route, status).Inc()
		httpDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

// WriteHeader captures the status code so Middleware can report it in metrics.
func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}
