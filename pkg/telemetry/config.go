// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"os"
	"strconv"
	"time"
)

// DefaultEndpoint is the single, hard-coded receiver this project
// operates. It is deliberately not configurable: letting operators
// point telemetry at an arbitrary URL is the first step toward
// "add a Datadog/New Relic exporter", which is out of scope and a
// support-burden trap (anti-pattern 93). Operators have exactly two
// choices — enabled or not.
const DefaultEndpoint = "https://telemetry.kubeatlas.dev/v1/report"

// DefaultInterval is how often a report is sent once enabled.
const DefaultInterval = 24 * time.Hour

// Config is the telemetry feature's runtime configuration. The zero
// value is disabled — the safe default (invariant 2.3).
type Config struct {
	Enabled  bool
	Endpoint string
	Interval time.Duration
}

// LoadConfig reads the telemetry settings from the environment. The
// Helm chart sets KUBEATLAS_TELEMETRY_ENABLED from telemetry.enabled
// (default false). The endpoint is NOT read from the environment —
// it is the hard-coded constant so it stays auditable.
//
// KUBEATLAS_TELEMETRY_INTERVAL_SECONDS is honoured only to let the
// test/chaos harness shorten the cadence; it is not a documented
// operator knob.
func LoadConfig() Config {
	cfg := Config{
		Enabled:  os.Getenv("KUBEATLAS_TELEMETRY_ENABLED") == "true",
		Endpoint: DefaultEndpoint,
		Interval: DefaultInterval,
	}
	if v := os.Getenv("KUBEATLAS_TELEMETRY_INTERVAL_SECONDS"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			cfg.Interval = time.Duration(secs) * time.Second
		}
	}
	return cfg
}
