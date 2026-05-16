package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/extractor/rego"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/snapshot"
)

// DefaultAddr is the listen address when one isn't supplied to New.
const DefaultAddr = ":8080"

// shutdownGrace is how long Start gives in-flight requests to finish
// after ctx is cancelled.
const shutdownGrace = 10 * time.Second

// Server is the long-running HTTP service that exposes the graph.
//
// Phase 1 W5 wires the skeleton (this file) — `/healthz`, `/readyz`,
// route registration, and the middleware chain. The eight v1alpha1
// REST handlers ship in W6 (P1-T6); the WebSocket watch endpoint and
// readiness gating land in W5/W9.
type Server struct {
	addr      string
	store     graph.GraphStore
	aggs      *aggregator.Registry
	hub       *WatchHub
	readiness *ReadinessGate
	metrics   *metricsCounter

	// regoMetrics + regoModuleCount are the Phase 2 hooks for the
	// rego engine's atomic counters and live module count. main.go
	// wires them through WithRegoMetrics so /metrics surfaces
	// engine state (cache hits/misses/timeouts/panics + loaded
	// module gauge).
	regoMetrics     *rego.Metrics
	regoModuleCount func() int

	// snapshotMetrics + snapshotQueueDepth are the Phase 3 F-111
	// hooks. main.go wires them through WithSnapshotMetrics, but
	// only when the snapshot writer is running (Tier 2 +
	// snapshots.enabled) — left nil otherwise so /metrics stays
	// snapshot-block-free on a Tier 1 install.
	snapshotMetrics    *snapshot.Metrics
	snapshotQueueDepth func() int

	// snapshotsEnabled + snapshotRetention drive the F-111
	// /api/v1/snapshots endpoints. The API server cannot tell its
	// store's tier on its own, so main.go calls WithSnapshots only
	// when the snapshot feature is actually active (Tier 2 +
	// snapshots.enabled). When false the snapshot handlers return
	// 503 (invariant 2.2: no Tier 1 snapshots). snapshotRetention
	// caps how wide a diff window the diff endpoint accepts.
	snapshotsEnabled  bool
	snapshotRetention time.Duration

	// webFS, when non-nil, is mounted at "/" to serve the embedded
	// Web UI bundle. Tests leave it nil so the catch-all static
	// route stays inactive.
	webFS fs.FS

	// httpSrv is created in Start; nil before then.
	httpSrv *http.Server
}

// ServerOption tweaks an optional aspect of the Server. Required
// dependencies stay positional; additive features (the embedded Web
// UI, future hooks) ride on options so existing call sites and tests
// don't break each time something is added.
type ServerOption func(*Server)

// WithWebFS mounts the given filesystem under "/" so the Web UI
// bundle can be served from the same binary as the API. The handler
// expects a Vite-shaped layout (assets/, index.html at the root).
// Unknown paths fall back to index.html so the SPA's client-side
// router can take over — standard pattern for hash-less routing.
func WithWebFS(f fs.FS) ServerOption {
	return func(s *Server) { s.webFS = f }
}

// WithRegoMetrics wires the rego engine's metrics + module-count
// callable into /metrics rendering. nil callables turn off the
// corresponding lines (Phase 1 behavior preserved when no engine is
// wired).
func WithRegoMetrics(m *rego.Metrics, moduleCount func() int) ServerOption {
	return func(s *Server) {
		s.regoMetrics = m
		s.regoModuleCount = moduleCount
	}
}

// WithSnapshotMetrics wires the F-111 snapshot writer's counters +
// live queue-depth callable into /metrics. main.go passes this only
// when the writer is running (Tier 2 + snapshots.enabled); a Tier 1
// install leaves both nil and /metrics emits no snapshot block.
func WithSnapshotMetrics(m *snapshot.Metrics, queueDepth func() int) ServerOption {
	return func(s *Server) {
		s.snapshotMetrics = m
		s.snapshotQueueDepth = queueDepth
	}
}

// WithSnapshots marks the F-111 snapshot feature active and sets
// the retention window. main.go calls it only on a Tier 2 install
// with snapshots.enabled; without it the /api/v1/snapshots
// endpoints return 503 (invariant 2.2). retention caps the diff
// window — a diff wider than what the store retains is rejected.
func WithSnapshots(retention time.Duration) ServerOption {
	return func(s *Server) {
		s.snapshotsEnabled = true
		s.snapshotRetention = retention
	}
}

// New builds a Server. addr defaults to ":8080" if empty. The server
// owns its WatchHub, ReadinessGate, and metrics counter; callers that
// need to drive any of them (e.g. the informer flipping readiness)
// reach in via Hub() / Readiness(). Pass WithWebFS(...) to enable the
// embedded Web UI mount.
func New(addr string, store graph.GraphStore, aggs *aggregator.Registry, opts ...ServerOption) *Server {
	if addr == "" {
		addr = DefaultAddr
	}
	s := &Server{
		addr:      addr,
		store:     store,
		aggs:      aggs,
		hub:       NewWatchHub(),
		readiness: NewReadinessGate(),
		metrics:   newMetricsCounter(),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Hub returns the WatchHub the server registers on
// /api/v1alpha1/watch. Callers like the informer pipeline use it to
// Broadcast graph updates.
func (s *Server) Hub() *WatchHub { return s.hub }

// Readiness returns the ReadinessGate. The informer manager calls
// MarkReady() on it once the cache has finished its initial sync.
func (s *Server) Readiness() *ReadinessGate { return s.readiness }

// Start boots the HTTP listener and blocks until ctx is cancelled or
// the listener fails. On ctx.Done() it triggers a graceful shutdown
// with up to shutdownGrace for in-flight requests to drain. Returns
// ctx.Err() on a clean shutdown so callers can distinguish "we asked
// the server to stop" from "the server crashed".
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	handler := chain(mux,
		recoveryMiddleware,
		metricsMiddleware(s.metrics),
		accessLogMiddleware,
		corsMiddleware,
	)

	s.httpSrv = &http.Server{
		Addr:              s.addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("api server starting", "addr", s.addr)
		if err := s.httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		// Drain the listener goroutine so the caller doesn't have to
		// guess about goroutine cleanup.
		<-errCh
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// Addr reports the listen address. Convenient for tests that bind to
// :0 and then need to know which port they got.
func (s *Server) Addr() string {
	if s.httpSrv != nil && s.httpSrv.Addr != "" {
		return s.httpSrv.Addr
	}
	return s.addr
}

// registerRoutes binds every API endpoint by walking the route table
// from Routes(). Routes() is the single source of truth for both
// registration and OpenAPI emission, so the spec can't drift from
// what the server serves.
//
// The static "/" mount for the embedded Web UI is added only when
// WithWebFS gave the server a non-nil filesystem — otherwise tests
// (which leave it nil) keep their 404-on-unknown-route behaviour.
// Net/http 1.22 mux gives more specific patterns priority, so the
// "/" handler doesn't shadow "/api/v1alpha1/...".
func (s *Server) registerRoutes(mux *http.ServeMux) {
	for _, r := range s.Routes() {
		// Routes() lists patterns in v1alpha1 form (the original
		// Phase 1 surface). Register each /api/v1alpha1/... entry
		// under /api/v1/... too so v1 GA serves byte-identical
		// behaviour with version-aware serialisation. Unversioned
		// routes (/healthz, /readyz, /metrics) hit the
		// versionedPattern early-return and bind exactly once.
		mux.HandleFunc(r.Method+" "+r.Pattern, r.handler)
		if v1 := versionedPattern(r.Pattern, APIVersionV1); v1 != r.Pattern {
			mux.HandleFunc(r.Method+" "+v1, r.handler)
		}
	}
	if s.webFS != nil {
		mux.Handle("GET /", s.staticHandler())
	}
}

// staticHandler serves the embedded Web UI. It tries the requested
// path first; on a miss it falls back to index.html so the React
// router can resolve client-side routes like /resources/.../...
// without each one needing a server-side mapping.
func (s *Server) staticHandler() http.Handler {
	sub, err := fs.Sub(s.webFS, "web/dist")
	if err != nil {
		// fs.Sub only fails on a malformed path; the literal above
		// is fine, so this is unreachable. Be loud anyway.
		slog.Error("staticHandler: fs.Sub failed", "err", err)
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			clean = "index.html"
		}
		f, err := sub.Open(clean)
		if err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback. If even index.html is missing the bundle
		// wasn't built into the binary — return 404 so the failure
		// is loud rather than mysterious.
		idx, err := sub.Open("index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer func() { _ = idx.Close() }()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.Copy(w, idx)
	})
}

// handleHealth is the liveness probe. Returns 200 unless the process
// is broken enough that this code can't run.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady is the readiness probe. Returns 503 until the informer
// flips the gate to ready, then 200.
func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	if !s.readiness.IsReady() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "not_ready",
			"reason": "informer cache has not completed initial sync",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// writeJSON is the success-path counterpart to writeError.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
