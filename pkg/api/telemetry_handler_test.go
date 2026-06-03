// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/telemetry"
)

func TestTelemetryHandlers(t *testing.T) {
	collector := telemetry.NewCollector("v1.4.0-test", telemetry.Providers{
		Tier:          func() string { return "memory" },
		ResourceCount: func(context.Context) (int, error) { return 42, nil },
	})
	// Disabled (the default): /preview must still work (transparency),
	// and /status must report disabled.
	sender := telemetry.NewSender(
		telemetry.Config{Enabled: false, Endpoint: telemetry.DefaultEndpoint},
		collector, nil,
	)

	base, _, cleanup := seedAndServe(t, nil, api.WithTelemetry(sender))
	defer cleanup()

	var status struct {
		Enabled  bool   `json:"enabled"`
		Endpoint string `json:"endpoint"`
	}
	resp, body := getJSON(t, base+"/api/v1/telemetry/status", &status)
	if resp.StatusCode != 200 {
		t.Fatalf("status code = %d (%s)", resp.StatusCode, body)
	}
	if status.Enabled {
		t.Error("telemetry should report disabled by default")
	}

	resp, body = getJSON(t, base+"/api/v1/telemetry/preview", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("preview status = %d (%s)", resp.StatusCode, body)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("preview not JSON: %v", err)
	}
	if m["schema_version"] == nil || m["kubeatlas_version"] != "v1.4.0-test" {
		t.Errorf("preview payload unexpected: %s", body)
	}
	for _, bad := range []string{"namespace", "install_uuid", "ip", "resource_name"} {
		if _, leaked := m[bad]; leaked {
			t.Errorf("preview leaked sensitive field %q", bad)
		}
	}
}

func TestTelemetryHandlers_NotConfigured(t *testing.T) {
	// No WithTelemetry: status reports disabled, preview is 503.
	base, _, cleanup := seedAndServe(t, nil)
	defer cleanup()

	var status struct {
		Enabled bool `json:"enabled"`
	}
	getJSON(t, base+"/api/v1/telemetry/status", &status)
	if status.Enabled {
		t.Error("status should be disabled when telemetry is not wired")
	}
	resp, _ := getJSON(t, base+"/api/v1/telemetry/preview", nil)
	if resp.StatusCode != 503 {
		t.Errorf("preview status = %d, want 503 when not configured", resp.StatusCode)
	}
}
