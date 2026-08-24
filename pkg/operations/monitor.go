// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Package operations tracks product-neutral dependency health for operators.
// It never mutates Kubernetes or PostgreSQL: probes are bounded read-only
// calls, and the optional backup marker is a read-only file maintained by the
// operator's backup workflow.
package operations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultProbeInterval = 15 * time.Second
	DefaultProbeTimeout  = 5 * time.Second
	DefaultStaleAfter    = 2 * time.Minute
	maxBackupMarkerBytes = 128
)

// ObservationState is the operator-facing state of the Kubernetes graph.
type ObservationState string

const (
	ObservationInitializing ObservationState = "initializing"
	ObservationSynced       ObservationState = "synced"
	ObservationDegraded     ObservationState = "degraded"
	ObservationStale        ObservationState = "stale"
)

// Probe is one bounded, read-only dependency check.
type Probe func(context.Context) error

// Config controls the detached operational monitor.
type Config struct {
	ProbeInterval       time.Duration
	ProbeTimeout        time.Duration
	StaleAfter          time.Duration
	BackupTimestampFile string
	StorageDurable      bool
}

// LoadConfig reads the operational monitor configuration from the
// environment. Invalid durations fail startup instead of silently disabling
// production signals.
func LoadConfig() (Config, error) {
	cfg := Config{
		ProbeInterval:       DefaultProbeInterval,
		ProbeTimeout:        DefaultProbeTimeout,
		StaleAfter:          DefaultStaleAfter,
		BackupTimestampFile: strings.TrimSpace(os.Getenv("KUBEATLAS_BACKUP_TIMESTAMP_FILE")),
	}
	var err error
	if cfg.ProbeInterval, err = envDuration("KUBEATLAS_OPERATIONS_PROBE_INTERVAL", cfg.ProbeInterval); err != nil {
		return Config{}, err
	}
	if cfg.ProbeTimeout, err = envDuration("KUBEATLAS_OPERATIONS_PROBE_TIMEOUT", cfg.ProbeTimeout); err != nil {
		return Config{}, err
	}
	if cfg.StaleAfter, err = envDuration("KUBEATLAS_OPERATIONS_STALE_AFTER", cfg.StaleAfter); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("operations: %s must be a positive Go duration", name)
	}
	return d, nil
}

// Snapshot is a lock-free-for-callers copy of the latest monitor state.
// It contains no resource names, namespaces, credentials, or probe errors.
type Snapshot struct {
	KubernetesKnown       bool
	KubernetesReachable   bool
	KubernetesLastSuccess time.Time
	StorageKnown          bool
	StorageReachable      bool
	StorageDurable        bool
	StorageLastSuccess    time.Time
	BackupStatusAvailable bool
	BackupLastSuccess     time.Time
	StaleAfter            time.Duration
}

// ObservationState derives the graph state without treating a quiet cluster
// as stale. Freshness comes from a successful Kubernetes API probe, not the
// time of the most recent resource event.
func (s Snapshot) ObservationState(initialSync bool, now time.Time) ObservationState {
	if !initialSync || !s.KubernetesKnown {
		return ObservationInitializing
	}
	if s.KubernetesReachable {
		return ObservationSynced
	}
	if s.KubernetesLastSuccess.IsZero() || now.Sub(s.KubernetesLastSuccess) >= s.StaleAfter {
		return ObservationStale
	}
	return ObservationDegraded
}

// Monitor runs detached from request and informer hot paths.
type Monitor struct {
	cfg             Config
	kubernetesProbe Probe
	storageProbe    Probe

	mu       sync.RWMutex
	snapshot Snapshot
}

// New returns a monitor with explicit read-only dependency probes.
func New(cfg Config, kubernetesProbe, storageProbe Probe) *Monitor {
	if cfg.ProbeInterval <= 0 {
		cfg.ProbeInterval = DefaultProbeInterval
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = DefaultProbeTimeout
	}
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = DefaultStaleAfter
	}
	if kubernetesProbe == nil {
		kubernetesProbe = func(context.Context) error { return errors.New("kubernetes probe is not configured") }
	}
	if storageProbe == nil {
		storageProbe = func(context.Context) error { return errors.New("storage probe is not configured") }
	}
	return &Monitor{
		cfg:             cfg,
		kubernetesProbe: kubernetesProbe,
		storageProbe:    storageProbe,
		snapshot: Snapshot{
			StorageDurable: cfg.StorageDurable,
			StaleAfter:     cfg.StaleAfter,
		},
	}
}

// Start samples immediately, then at the configured fixed interval until the
// parent context is cancelled.
func (m *Monitor) Start(ctx context.Context) {
	m.sample(ctx, time.Now())
	ticker := time.NewTicker(m.cfg.ProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			m.sample(ctx, now)
		}
	}
}

// Snapshot returns the latest state without exposing probe errors.
func (m *Monitor) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshot
}

func (m *Monitor) sample(ctx context.Context, now time.Time) {
	kubernetesOK := m.runProbe(ctx, m.kubernetesProbe)
	storageOK := m.runProbe(ctx, m.storageProbe)
	backupTime, backupOK := readBackupTimestamp(m.cfg.BackupTimestampFile, now)

	m.mu.Lock()
	previous := m.snapshot
	m.snapshot.KubernetesKnown = true
	m.snapshot.KubernetesReachable = kubernetesOK
	if kubernetesOK {
		m.snapshot.KubernetesLastSuccess = now
	}
	m.snapshot.StorageKnown = true
	m.snapshot.StorageReachable = storageOK
	if storageOK {
		m.snapshot.StorageLastSuccess = now
	}
	m.snapshot.BackupStatusAvailable = backupOK
	m.snapshot.BackupLastSuccess = backupTime
	current := m.snapshot
	m.mu.Unlock()

	logTransition("kubernetes_api", previous.KubernetesKnown, previous.KubernetesReachable,
		current.KubernetesReachable)
	logTransition("storage", previous.StorageKnown, previous.StorageReachable,
		current.StorageReachable)
	if previous.BackupStatusAvailable != current.BackupStatusAvailable {
		if current.BackupStatusAvailable {
			slog.Info("operational backup marker became available")
		} else if m.cfg.BackupTimestampFile != "" {
			slog.Warn("operational backup marker is unavailable or invalid")
		}
	}
}

func (m *Monitor) runProbe(ctx context.Context, probe Probe) bool {
	probeCtx, cancel := context.WithTimeout(ctx, m.cfg.ProbeTimeout)
	defer cancel()
	return probe(probeCtx) == nil
}

func logTransition(dependency string, wasKnown, wasReachable, reachable bool) {
	if wasKnown && wasReachable == reachable {
		return
	}
	if reachable {
		slog.Info("operational dependency reachable", "dependency", dependency)
		return
	}
	slog.Warn("operational dependency unreachable", "dependency", dependency)
}

func readBackupTimestamp(path string, now time.Time) (time.Time, bool) {
	if path == "" {
		return time.Time{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer func() { _ = f.Close() }()

	body, err := io.ReadAll(io.LimitReader(f, maxBackupMarkerBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxBackupMarkerBytes {
		return time.Time{}, false
	}
	raw := strings.TrimSpace(string(body))
	var timestamp time.Time
	if unix, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if unix <= 0 {
			return time.Time{}, false
		}
		timestamp = time.Unix(unix, 0).UTC()
	} else {
		var parseErr error
		timestamp, parseErr = time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			return time.Time{}, false
		}
	}
	if timestamp.After(now.Add(time.Minute)) {
		return time.Time{}, false
	}
	return timestamp, true
}
