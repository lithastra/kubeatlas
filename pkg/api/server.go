package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph"
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
	addr  string
	store graph.GraphStore
	aggs  *aggregator.Registry

	// httpSrv is created in Start; nil before then.
	httpSrv *http.Server
}

// New builds a Server. addr defaults to ":8080" if empty.
func New(addr string, store graph.GraphStore, aggs *aggregator.Registry) *Server {
	if addr == "" {
		addr = DefaultAddr
	}
	return &Server{
		addr:  addr,
		store: store,
		aggs:  aggs,
	}
}

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

// registerRoutes binds every API endpoint. Phase 1 W5 only registers
// the operational endpoints (health/readiness); the v1alpha1 graph
// endpoints land in P1-T6.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleReady)
}

// handleHealth is the liveness probe. Returns 200 unless the process
// is broken enough that this code can't run.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady is the readiness probe. In W5 it always returns 200;
// W6's full implementation will gate on informer-cache sync via a
// readiness flag the informer manager flips.
func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// writeJSON is the success-path counterpart to writeError.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
