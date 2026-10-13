package httpx

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"smartledger/internal/secure"
)

type Middleware func(http.Handler) http.Handler

// Chain memasang middleware; yang pertama dalam daftar menjadi lapisan terluar.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

type ridKey struct{}

func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ridKey{}).(string)
	return id
}

var ridRe = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !ridRe.MatchString(id) {
			id = secure.NewUUID()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ridKey{}, id)))
	})
}

// Recover mengubah panic menjadi 500 generik; stack trace hanya ke log.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.ErrorContext(r.Context(), "panic", "request_id", RequestIDFrom(r.Context()),
					"panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
				WriteError(w, r, fmt.Errorf("panic: %v", rec))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// AccessLog mencatat satu baris per request. Sengaja TIDAK mencatat query string,
// header, atau body supaya PIN/token/data keuangan tidak pernah masuk log.
func AccessLog(hops int) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			slog.InfoContext(r.Context(), "http",
				"request_id", RequestIDFrom(r.Context()), "method", r.Method, "path", r.URL.Path,
				"status", rec.status, "ms", time.Since(start).Milliseconds(), "ip", ClientIP(r, hops))
		})
	}
}

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// Timeout membatasi waktu satu request; query database ikut dibatalkan lewat context.
func Timeout(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClientIP: hops=0 memakai RemoteAddr. hops=N mempercayai N proxy di depan dan
// mengambil entri ke-N dari KANAN X-Forwarded-For (entri kiri bisa dipalsukan klien).
func ClientIP(r *http.Request, hops int) string {
	if hops > 0 {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if idx := len(parts) - hops; idx >= 0 {
			if ip := strings.TrimSpace(parts[idx]); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
