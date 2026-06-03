package api

import (
	"bufio"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// Middleware is the standard http.Handler wrapper signature.
type Middleware func(http.Handler) http.Handler

// chain composes middlewares so they run outermost-first; the first
// middleware in mws sees the request before the rest.
func chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// recoveryMiddleware turns panics in downstream handlers into a clean
// 500 + JSON body and a logged stack trace, so a single buggy handler
// can't take the server down.
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic in handler",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				writeError(w, http.StatusInternalServerError, CodeInternal, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// accessLogMiddleware emits one slog.Info line per request with method,
// path, status, and duration. Status capture goes through a
// statusRecorder that wraps the writer.
func accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// metricsMiddleware increments the request counter once the inner
// handler has finished, so the recorded status reflects what was
// actually written. Skips its own /metrics path so scrapes don't
// inflate their own counter.
func metricsMiddleware(c *metricsCounter) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			if r.URL.Path == "/metrics" {
				return
			}
			c.inc(r.Method, rec.status)
		})
	}
}

// versionMetricsMiddleware records one count per request against the
// API version in its path (v1alpha1 / v1) and the matched route's
// endpoint. It runs after the inner handler so r.Pattern (the matched
// route) is set. This is the data source for the v1alpha1 retirement
// decision; it records no caller identity.
func versionMetricsMiddleware(vc *versionCounter) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			switch {
			case strings.HasPrefix(r.URL.Path, apiPrefixV1Alpha1):
				vc.inc("v1alpha1", versionEndpoint(r.Pattern, apiPrefixV1Alpha1))
			case strings.HasPrefix(r.URL.Path, apiPrefixV1):
				vc.inc("v1", versionEndpoint(r.Pattern, apiPrefixV1))
			}
		})
	}
}

// versionEndpoint reduces a matched route pattern ("GET /api/v1/graph")
// to a low-cardinality endpoint label ("graph"). Unmatched requests
// (404s, where the pattern is empty) report "other".
func versionEndpoint(pattern, prefix string) string {
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		pattern = pattern[i+1:]
	}
	ep := strings.TrimPrefix(pattern, prefix)
	if ep == "" || ep == pattern {
		return "other"
	}
	return ep
}

// corsMiddleware permits cross-origin requests from any origin. The
// Phase 1 deployment story is "stand the API up behind an Ingress that
// owns the auth + origin policy", so the server itself is permissive.
// Production hardening lives at the Ingress layer.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder wraps http.ResponseWriter so the access log sees the
// status code that was actually written. It forwards Hijack so the
// WebSocket upgrade in pkg/api/websocket.go still works through the
// middleware chain, and Flush so future SSE / streaming endpoints
// aren't blocked on the wrapper.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return hj.Hijack()
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
