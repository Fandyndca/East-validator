package api

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// maxBodyBytes rejects oversized write payloads (DoS).
const maxBodyBytes = 1 << 20 // 1 MiB

// ipRateLimiter is a simple fixed-window per-IP limiter for public endpoints.
type ipRateLimiter struct {
	mu      sync.Mutex
	hits    map[string][]time.Time
	max     int
	window  time.Duration
}

func newIPRateLimiter(max int, window time.Duration) *ipRateLimiter {
	rl := &ipRateLimiter{
		hits:   make(map[string][]time.Time),
		max:    max,
		window: window,
	}
	go func() {
		ticker := time.NewTicker(window)
		defer ticker.Stop()
		for range ticker.C {
			rl.mu.Lock()
			cutoff := time.Now().Add(-window)
			for ip, ts := range rl.hits {
				kept := ts[:0]
				for _, t := range ts {
					if t.After(cutoff) {
						kept = append(kept, t)
					}
				}
				if len(kept) == 0 {
					delete(rl.hits, ip)
				} else {
					rl.hits[ip] = kept
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

func (rl *ipRateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)
	recent := rl.hits[ip][:0]
	for _, t := range rl.hits[ip] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= rl.max {
		rl.hits[ip] = recent
		return false
	}
	rl.hits[ip] = append(recent, now)
	return true
}

func clientIP(r *http.Request) string {
	// Railway / reverse proxies
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// first hop
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// withPublicGuards wraps the mux with:
//   - global per-IP rate limit (default 120 req/min)
//   - MaxBytesReader on request bodies
//   - basic security headers
func withPublicGuards(next http.Handler) http.Handler {
	// 120 requests / minute / IP is generous for explorers + light clients
	// but stops trivial flood from a single source.
	rl := newIPRateLimiter(120, time.Minute)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if !rl.allow(ip) {
			log.Warn().Str("ip", ip).Str("path", r.URL.Path).Msg("rate limited")
			http.Error(w, `{"ok":false,"error":"rate_limited"}`, http.StatusTooManyRequests)
			return
		}

		// Security headers (public-facing API)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		// CORS: allow read from browser wallets / explorers. Writes still need
		// X-API-Secret so open CORS on GET is acceptable for a public chain RPC.
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Secret")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")

		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}
