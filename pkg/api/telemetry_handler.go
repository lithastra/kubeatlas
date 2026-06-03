// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/lithastra/kubeatlas/pkg/telemetry"
)

// TelemetryProvider is the slice of the telemetry sender the API needs.
// *telemetry.Sender satisfies it; the interface keeps the handler
// testable with a stub.
type TelemetryProvider interface {
	Enabled() bool
	Endpoint() string
	Interval() time.Duration
	LastSent() (time.Time, bool)
	Preview(ctx context.Context) telemetry.Payload
	Metrics() *telemetry.Metrics
}

// WithTelemetry wires the telemetry sender into the API so the
// /api/v1/telemetry/* endpoints and the /metrics telemetry block can
// report on it. The collector is always wired (even when disabled) so
// /preview can show what would be sent before a user opts in.
func WithTelemetry(t TelemetryProvider) ServerOption {
	return func(s *Server) { s.telemetry = t }
}

// TelemetryStatusResponse is the body of GET /api/v1/telemetry/status.
type TelemetryStatusResponse struct {
	Enabled  bool       `json:"enabled"`
	Endpoint string     `json:"endpoint"`
	LastSent *time.Time `json:"last_sent,omitempty"`
	NextSend *time.Time `json:"next_send,omitempty"`
}

// handleTelemetryStatus reports whether telemetry is on, where it sends,
// and the last/next send times.
func (s *Server) handleTelemetryStatus(w http.ResponseWriter, r *http.Request) {
	if s.telemetry == nil {
		writeJSON(w, http.StatusOK, TelemetryStatusResponse{Enabled: false})
		return
	}
	resp := TelemetryStatusResponse{
		Enabled:  s.telemetry.Enabled(),
		Endpoint: s.telemetry.Endpoint(),
	}
	if last, ok := s.telemetry.LastSent(); ok {
		next := last.Add(s.telemetry.Interval())
		resp.LastSent = &last
		resp.NextSend = &next
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleTelemetryPreview returns the exact payload the next report would
// send — the transparency contract. Works whether or not telemetry is
// enabled.
func (s *Server) handleTelemetryPreview(w http.ResponseWriter, r *http.Request) {
	if s.telemetry == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "telemetry is not configured on this server")
		return
	}
	writeJSON(w, http.StatusOK, s.telemetry.Preview(r.Context()))
}
