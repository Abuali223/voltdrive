// HTTP middleware: panic recovery, request IDs, structured access logging,
// security headers, an origin allowlist for CORS, and per-client rate limiting.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type ctxKey string

const reqIDKey ctxKey = "reqid"

// chain applies middleware so that mw[0] is the outermost wrapper.
func chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// statusRecorder captures the response status code for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(c int) { s.status = c; s.ResponseWriter.WriteHeader(c) }
func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Hijack/Flush passthrough so WebSocket upgrades still work behind the recorder.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// recoverMW turns a panic in any handler into a 500 instead of crashing.
func recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic recovered: %v (%s %s)", rec, r.Method, r.URL.Path)
				writeErr(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestID attaches a short id to the context and the X-Request-ID header.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), reqIDKey, id)))
	})
}

// accessLog logs method, path, status, duration, client IP and request id.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		log.Printf("%s %s %d %s ip=%s id=%v", r.Method, r.URL.Path, rec.status,
			time.Since(start).Round(time.Millisecond), clientIP(r), r.Context().Value(reqIDKey))
	})
}

// securityHeaders sets conservative response headers on the API.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// corsAllowlist reflects the Origin only when it is in the allowlist. An empty
// allowlist falls back to permissive "*" (dev convenience).
func corsAllowlist(allowed []string) func(http.Handler) http.Handler {
	allow := map[string]bool{}
	for _, o := range allowed {
		allow[strings.TrimRight(o, "/")] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			switch {
			case len(allow) == 0:
				w.Header().Set("Access-Control-Allow-Origin", "*")
			case origin != "" && allow[origin]:
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Max-Age", "600")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// --- per-client rate limiter (token bucket) ---

type bucket struct {
	tokens float64
	last   time.Time
}

type limiter struct {
	mu    sync.Mutex
	b     map[string]*bucket
	rate  float64 // tokens per second
	burst float64
}

func newLimiter(rate, burst float64) *limiter {
	l := &limiter{b: map[string]*bucket{}, rate: rate, burst: burst}
	go l.gc()
	return l
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	bk := l.b[key]
	if bk == nil {
		bk = &bucket{tokens: l.burst, last: now}
		l.b[key] = bk
	}
	bk.tokens += now.Sub(bk.last).Seconds() * l.rate
	if bk.tokens > l.burst {
		bk.tokens = l.burst
	}
	bk.last = now
	if bk.tokens >= 1 {
		bk.tokens--
		return true
	}
	return false
}

// gc drops idle buckets so the map cannot grow without bound.
func (l *limiter) gc() {
	t := time.NewTicker(5 * time.Minute)
	for range t.C {
		l.mu.Lock()
		cut := time.Now().Add(-10 * time.Minute)
		for k, bk := range l.b {
			if bk.last.Before(cut) {
				delete(l.b, k)
			}
		}
		l.mu.Unlock()
	}
}

func (l *limiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health checks are never rate limited.
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		if !l.allow(clientIP(r)) {
			w.Header().Set("Retry-After", "1")
			writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// trustedProxyHops is the number of trusted reverse proxies between this
// service and the public internet (set from Server.TrustedProxyHops). On Cloud
// Run with default ingress this is 0 — the platform appends the real peer as
// the right-most X-Forwarded-For entry, so the leftmost (client-supplied,
// spoofable) entries are ignored.
var trustedProxyHops int

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		// Walk in from the right by the number of trusted hops. The leftmost
		// values are attacker-controlled and must never be used as the rate-
		// limit / abuse key.
		idx := len(parts) - 1 - trustedProxyHops
		if idx < 0 {
			idx = 0
		}
		if ip := strings.TrimSpace(parts[idx]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
