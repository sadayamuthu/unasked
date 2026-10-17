// Package ratelimit limits public API requests per client in fixed windows.
package ratelimit

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Limits are the requests allowed per window for anonymous and keyed callers.
type Limits struct {
	Anon, Keyed int
	Window      time.Duration
}

// Limiter counts requests per client in fixed windows.
type Limiter struct {
	mu     sync.Mutex
	limits Limits
	counts map[string]int
	reset  time.Time
	now    func() time.Time
}

// New returns a Limiter enforcing l.
func New(l Limits) *Limiter {
	return &Limiter{limits: l, counts: map[string]int{}, now: time.Now}
}

// Allow records one request for key and reports whether it is within the
// limit and, if not, how long until the window resets.
func (l *Limiter) Allow(key string, keyed bool) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.After(l.reset) {
		l.counts, l.reset = map[string]int{}, now.Add(l.limits.Window)
	}
	limit := l.limits.Anon
	if keyed {
		limit = l.limits.Keyed
	}
	l.counts[key]++
	if l.counts[key] > limit {
		return false, l.reset.Sub(now)
	}
	return true, 0
}

// Middleware rejects requests over the limit with 429 Too Many Requests and a
// Retry-After header in seconds.
func Middleware(l *Limiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, keyed := r.Header.Get("X-Api-Key"), true
		if key == "" {
			key, keyed = r.RemoteAddr, false
		}
		if ok, wait := l.Allow(key, keyed); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
