// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Sender periodically POSTs a Payload to the telemetry endpoint when
// enabled. It is fire-and-forget: a send failure is logged and counted
// but never retried and never blocks anything (anti-pattern 89). When
// disabled, Run returns immediately so a caller can always `go
// sender.Run(ctx)` without special-casing.
type Sender struct {
	cfg       Config
	collector *Collector
	client    *http.Client
	logger    *slog.Logger
	metrics   *Metrics

	mu       sync.Mutex
	lastSent time.Time
	hasSent  bool
}

// NewSender builds a sender. A nil logger defaults to slog.Default().
func NewSender(cfg Config, collector *Collector, logger *slog.Logger) *Sender {
	if logger == nil {
		logger = slog.Default()
	}
	return &Sender{
		cfg:       cfg,
		collector: collector,
		client:    &http.Client{Timeout: 10 * time.Second},
		logger:    logger,
		metrics:   NewMetrics(),
	}
}

// Run drives the send loop until ctx is cancelled. Disabled is a no-op
// (returns nil immediately), so it is safe to launch unconditionally in
// a background goroutine — it must NOT be one of runWatch's blocking
// components, or a disabled telemetry would shut the server down.
func (s *Sender) Run(ctx context.Context) error {
	if !s.cfg.Enabled {
		s.logger.Info("telemetry disabled (opt in with telemetry.enabled=true)")
		return nil
	}
	s.logEnabled()

	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.send(ctx)
		}
	}
}

func (s *Sender) logEnabled() {
	s.logger.Info("Telemetry enabled.",
		"endpoint", s.cfg.Endpoint,
		"preview", "/api/v1/telemetry/preview",
		"disable", "--set telemetry.enabled=false")
}

// send builds and POSTs one report. All failure paths log + count and
// return; none retry.
func (s *Sender) send(ctx context.Context) {
	body, err := json.Marshal(s.collector.Collect(ctx))
	if err != nil {
		s.metrics.incErrors()
		s.logger.Warn("telemetry marshal failed", "err", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		s.metrics.incErrors()
		s.logger.Warn("telemetry request build failed", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		s.metrics.incErrors()
		s.logger.Warn("telemetry send failed", "err", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		s.metrics.incErrors()
		s.logger.Warn("telemetry send returned non-2xx", "status", resp.StatusCode)
		return
	}

	s.metrics.incSent()
	s.mu.Lock()
	s.lastSent = time.Now()
	s.hasSent = true
	s.mu.Unlock()
}

// --- read-only accessors used by the API handler + /metrics ---------

func (s *Sender) Enabled() bool           { return s.cfg.Enabled }
func (s *Sender) Endpoint() string        { return s.cfg.Endpoint }
func (s *Sender) Interval() time.Duration { return s.cfg.Interval }
func (s *Sender) Metrics() *Metrics       { return s.metrics }

// Preview returns the Payload that would be sent right now. Works
// regardless of Enabled so users can audit what telemetry would send
// before opting in.
func (s *Sender) Preview(ctx context.Context) Payload { return s.collector.Collect(ctx) }

// LastSent reports the last successful send time, or ok=false if none.
func (s *Sender) LastSent() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSent, s.hasSent
}
