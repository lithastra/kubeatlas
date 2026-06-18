// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Package otel implements the F-204 OpenTelemetry trace overlay's
// ingestion half: an OTLP/gRPC trace receiver, the span-retention
// worker, and their metrics. It accepts ONLY trace spans (no metrics,
// no logs), persists them to Tier 2 (PostgreSQL) through a narrow
// SpanSink seam, and is a complete no-op when disabled.
//
// It depends on raw OTLP protobuf (go.opentelemetry.io/proto/otlp) +
// standard grpc-go — never the OpenTelemetry Collector SDK (invariant
// 2.5).
package otel

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultGRPCAddr is the OTLP receiver's listen address. Port 4317
	// is the OTLP/gRPC convention; it is deliberately separate from
	// the 8080 HTTP API so the two can be network-policied apart.
	DefaultGRPCAddr = ":4317"
	// DefaultBufferSize bounds the receiver's drop-on-full span queue.
	DefaultBufferSize = 4096
	// DefaultRetention is how long spans are kept when otel.retention
	// is unset. Matches the Helm chart default.
	DefaultRetention = 7 * 24 * time.Hour
	// defaultPruneInterval is how often the retention worker sweeps.
	defaultPruneInterval = time.Hour
)

// Config is the receiver's runtime configuration, loaded from the
// process environment (the Helm chart renders KUBEATLAS_OTEL_* env
// from values.otel.*).
type Config struct {
	// Enabled gates the whole feature. When false the receiver never
	// listens and the retention worker never starts (zero overhead).
	Enabled bool
	// GRPCAddr is the OTLP/gRPC listen address (default ":4317").
	GRPCAddr string
	// BufferSize is the span-queue capacity; a full queue drops.
	BufferSize int
	// Retention is how long received spans are kept.
	Retention time.Duration
}

// LoadConfig reads KUBEATLAS_OTEL_* from the environment. A malformed
// value is an error so a typo'd Helm value fails the pod at startup
// rather than silently defaulting.
//
// Recognised variables:
//
//	KUBEATLAS_OTEL_ENABLED      "true" turns the receiver on (default off)
//	KUBEATLAS_OTEL_GRPC_ADDR    listen address (default ":4317")
//	KUBEATLAS_OTEL_BUFFER_SIZE  span-queue capacity (default 4096)
//	KUBEATLAS_OTEL_RETENTION    span retention, e.g. "7d" / "168h" (default 7d)
func LoadConfig() (Config, error) {
	cfg := Config{
		Enabled:    os.Getenv("KUBEATLAS_OTEL_ENABLED") == "true",
		GRPCAddr:   DefaultGRPCAddr,
		BufferSize: DefaultBufferSize,
		Retention:  DefaultRetention,
	}
	if v := os.Getenv("KUBEATLAS_OTEL_GRPC_ADDR"); v != "" {
		cfg.GRPCAddr = v
	}
	if v := os.Getenv("KUBEATLAS_OTEL_BUFFER_SIZE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("invalid KUBEATLAS_OTEL_BUFFER_SIZE %q: must be a positive integer", v)
		}
		cfg.BufferSize = n
	}
	ret, err := parseRetention(os.Getenv("KUBEATLAS_OTEL_RETENTION"))
	if err != nil {
		return Config{}, fmt.Errorf("KUBEATLAS_OTEL_RETENTION: %w", err)
	}
	cfg.Retention = ret
	return cfg, nil
}

// parseRetention accepts a plain day suffix ("7d") on top of
// everything time.ParseDuration handles ("168h"). Empty returns
// DefaultRetention. Mirrors snapshot.ParseRetention; duplicated here
// so the otel feature does not depend on the snapshot package.
func parseRetention(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultRetention, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid retention %q: day count must be a non-negative integer", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid retention %q: %w", s, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid retention %q: must not be negative", s)
	}
	return d, nil
}
