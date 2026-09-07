package ratelimit

import (
	"net"
	"net/http"
	"sync"
	"time"
)

type limiter struct {
	mu       sync.Mutex
	requests map[string][]time.Time
	limit    int
	window   time.Duration
}

// New returns a middleware that allows at most limit requests per window per IP.
func New(limit int, window time.Duration) func(http.Handler) http.Handler {
	l := &limiter{
		requests: make(map[string][]time.Time),
		limit:    limit,
		window:   window,
	}
	go l.cleanup()
	return l.middleware
}

// middleware counts requests per IP in a sliding window and blocks with 429 over the limit.
func (l *limiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)

		l.mu.Lock()
		now := time.Now()
		cutoff := now.Add(-l.window)
		prev := l.requests[ip]
		valid := prev[:0]
		for _, t := range prev {
			if t.After(cutoff) {
				valid = append(valid, t)
			}
		}
		valid = append(valid, now)
		l.requests[ip] = valid
		count := len(valid)
		l.mu.Unlock()

		if count > l.limit {
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cleanup periodically purges expired request timestamps to prevent unbounded memory growth.
func (l *limiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		l.mu.Lock()
		cutoff := time.Now().Add(-l.window)
		for ip, times := range l.requests {
			valid := times[:0]
			for _, t := range times {
				if t.After(cutoff) {
					valid = append(valid, t)
				}
			}
			if len(valid) == 0 {
				delete(l.requests, ip)
			} else {
				l.requests[ip] = valid
			}
		}
		l.mu.Unlock()
	}
}
