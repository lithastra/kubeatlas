// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestResourceBucket(t *testing.T) {
	cases := map[int]string{0: "<1K", 999: "<1K", 1000: "1K-5K", 4999: "1K-5K", 5000: "5K-10K", 9999: "5K-10K", 10000: ">10K", 50000: ">10K"}
	for n, want := range cases {
		if got := resourceBucket(n); got != want {
			t.Errorf("resourceBucket(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestCollector_BuildsPayload(t *testing.T) {
	c := NewCollector("v1.4.0", Providers{
		K8sVersion:    func() string { return "v1.30.0" },
		Tier:          func() string { return "postgres" },
		ResourceCount: func(context.Context) (int, error) { return 2500, nil },
		EnabledPacks:  func() []string { return []string{"openshift", "cert-manager"} },
		ClusterCount:  func() int { return 3 },
		Platforms:     func() map[string]int { return map[string]int{"vanilla": 3} },
	})
	p := c.Collect(context.Background())

	if p.SchemaVersion != SchemaVersion || p.KubeAtlasVersion != "v1.4.0" {
		t.Errorf("version fields = %q/%q", p.SchemaVersion, p.KubeAtlasVersion)
	}
	if p.K8sVersion != "v1.30.0" || p.Tier != "postgres" {
		t.Errorf("k8s/tier = %q/%q", p.K8sVersion, p.Tier)
	}
	if p.ResourceBucket != "1K-5K" {
		t.Errorf("resource bucket = %q, want 1K-5K", p.ResourceBucket)
	}
	if len(p.EnabledPacks) != 2 || p.ClusterCount != 3 {
		t.Errorf("packs/clusters = %v/%d", p.EnabledPacks, p.ClusterCount)
	}
	if p.SessionNonce == "" {
		t.Error("session nonce is empty")
	}
	if p.OS == "" || p.Arch == "" {
		t.Error("os/arch not filled")
	}
}

func TestCollector_NilProvidersAreSafe(t *testing.T) {
	p := NewCollector("v", Providers{}).Collect(context.Background())
	if p.EnabledPacks == nil || p.PlatformDistribution == nil {
		t.Error("slice/map fields should be non-nil for clean JSON")
	}
	if p.ResourceBucket != "<1K" {
		t.Errorf("default resource bucket = %q, want <1K", p.ResourceBucket)
	}
}

// TestPayload_OnlyAllowedFields enforces invariant 2.3: the wire payload
// must carry only the coarse, non-identifying fields — nothing that
// could name a resource, a namespace, or correlate sessions.
func TestPayload_OnlyAllowedFields(t *testing.T) {
	p := NewCollector("v", Providers{}).Collect(context.Background())
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"schema_version": true, "kubeatlas_version": true, "k8s_version": true,
		"os": true, "arch": true, "tier": true, "resource_bucket": true,
		"enabled_packs": true, "cluster_count": true, "platform_distribution": true,
		"session_nonce": true,
	}
	for k := range m {
		if !allowed[k] {
			t.Errorf("payload carries disallowed field %q", k)
		}
	}
	// Explicitly forbid known-bad keys even if someone adds them.
	for _, bad := range []string{"install_uuid", "namespace", "resource_name", "ip", "labels"} {
		if _, present := m[bad]; present {
			t.Errorf("payload must never carry %q", bad)
		}
	}
}

func TestSender_DisabledIsNoop(t *testing.T) {
	s := NewSender(Config{Enabled: false}, NewCollector("v", Providers{}), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // even cancelled, disabled must return nil promptly
	if err := s.Run(ctx); err != nil {
		t.Errorf("disabled Run = %v, want nil", err)
	}
}

func TestSender_SendsAndCountsErrors(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := NewSender(
		Config{Enabled: true, Endpoint: srv.URL, Interval: 10 * time.Millisecond},
		NewCollector("v", Providers{}), nil,
	)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = s.Run(ctx) }()

	waitFor(t, 3*time.Second, func() bool { return s.Metrics().Snapshot().Sent > 0 })
	cancel()
	if hits.Load() == 0 {
		t.Error("endpoint never received a report")
	}
	if _, ok := s.LastSent(); !ok {
		t.Error("LastSent should be set after a successful send")
	}

	// A non-2xx endpoint increments the error counter, not Sent.
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer badSrv.Close()
	bad := NewSender(
		Config{Enabled: true, Endpoint: badSrv.URL, Interval: 10 * time.Millisecond},
		NewCollector("v", Providers{}), nil,
	)
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() { _ = bad.Run(ctx2) }()
	waitFor(t, 3*time.Second, func() bool { return bad.Metrics().Snapshot().Errors > 0 })
	cancel2()
	if bad.Metrics().Snapshot().Sent != 0 {
		t.Error("non-2xx response should not count as sent")
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %s", timeout)
}
