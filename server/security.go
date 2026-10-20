package main

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// securityHeaders wraps every response with standard defense-in-depth
// HTTP headers (clickjacking/MIME-sniffing/referrer leakage protection)
// and, when TLS is active, HSTS so browsers remember to always use
// HTTPS for this host even if someone later links an http:// URL.
func securityHeaders(next http.Handler, tlsEnabled bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
		if tlsEnabled {
			h.Set("Strict-Transport-Security", "max-age=15552000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// loginLimiter is a small in-memory brute-force guard for /admin/login:
// after too many failed attempts from one source IP in a short window,
// further attempts are rejected (without even checking the password)
// until the window rolls over. Not a replacement for fail2ban-style
// tooling on an internet-facing host, but it raises the bar a lot above
// "unlimited password guesses" for an MVP with no other such tooling
// assumed to be in front of it.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: make(map[string][]time.Time)}
}

const (
	loginMaxAttempts = 8
	loginWindow      = 5 * time.Minute
)

// allow reports whether a new login attempt from ip should proceed. It
// always records the attempt call itself (call once per attempt,
// regardless of outcome — callers should call recordFailure only when
// the login actually fails, and allow before even checking the
// password, so a flood of attempts from one IP gets capped either way).
func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-loginWindow)
	kept := l.attempts[ip][:0]
	for _, t := range l.attempts[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	l.attempts[ip] = kept
	return len(kept) < loginMaxAttempts
}

func (l *loginLimiter) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts[ip] = append(l.attempts[ip], time.Now())
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}

// clientIP extracts just the IP (no port) from r.RemoteAddr, falling
// back to the raw value if it doesn't parse (shouldn't happen behind
// net/http, but better than panicking on a malformed value).
func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i != -1 && !strings.Contains(addr[i:], "]") {
		return addr[:i]
	}
	return addr
}
