// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package multicluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

// InformerStarter is the slice of *discovery.InformerManager
// multicluster.Manager depends on. The interface exists so the unit
// tests can inject a fake without standing up an apiserver.
type InformerStarter interface {
	Start(ctx context.Context) error
}

// InformerFactory builds an InformerStarter for a single member
// cluster. The Manager calls it once per AddCluster, handing it the
// per-cluster dynamic client and the operator-chosen cluster name
// (the future ClusterID on every Resource the returned informer
// produces).
//
// The default factory wires pkg/discovery.NewInformerManager with
// discovery.WithClusterID(clusterName). Tests pass a fake.
type InformerFactory func(clusterName string, dyn dynamic.Interface, store graph.GraphStore) InformerStarter

// DefaultInformerFactory is the production wiring: a stock
// pkg/discovery.InformerManager tagged with the cluster name. Callers
// that need extractors, broadcasters, or snapshot sinks build their
// own factory closing over those dependencies.
func DefaultInformerFactory(extraOpts ...discovery.InformerOption) InformerFactory {
	return func(clusterName string, dyn dynamic.Interface, store graph.GraphStore) InformerStarter {
		opts := append([]discovery.InformerOption{discovery.WithClusterID(clusterName)}, extraOpts...)
		return discovery.NewInformerManager(dyn, store, opts...)
	}
}

// Manager orchestrates per-cluster informers against a single shared
// GraphStore. Each member runs an independent informer pipeline tagged
// with ClusterID = the operator-chosen cluster name; failures inside
// one member are isolated and never crash another (invariant 2.4 +
// P3-T21 step 5).
type Manager struct {
	store   graph.GraphStore
	factory InformerFactory
	dial    dialer

	mu       sync.Mutex
	clusters map[string]*clusterEntry
}

// dialer turns kubeconfig bytes into a dynamic client. The real one
// goes through client-go; tests substitute a fake to avoid touching a
// cluster.
type dialer func(kubeconfig []byte) (dynamic.Interface, error)

func defaultDialer(kubeconfig []byte) (dynamic.Interface, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	return dynamic.NewForConfig(cfg)
}

// clusterEntry tracks one running member cluster.
type clusterEntry struct {
	name   string
	cancel context.CancelFunc
	done   chan struct{} // closed when the informer's goroutine returns
	err    error         // last error from Start (set after done closes)
}

// Option configures a Manager at construction time.
type Option func(*Manager)

// WithFactory overrides the InformerFactory the Manager uses to build
// per-cluster informers. The default factory wires
// pkg/discovery.NewInformerManager with WithClusterID set; production
// callers usually pass a factory that also configures the extractor
// registry, broadcaster, and snapshot sink.
func WithFactory(f InformerFactory) Option {
	return func(m *Manager) { m.factory = f }
}

// withDialer is unexported; tests use it via package-internal helpers.
func withDialer(d dialer) Option {
	return func(m *Manager) { m.dial = d }
}

// New returns a Manager that writes every member cluster's resources
// into store. Pass options to override the InformerFactory; the
// default factory wires pkg/discovery with WithClusterID only.
func New(store graph.GraphStore, opts ...Option) *Manager {
	m := &Manager{
		store:    store,
		factory:  DefaultInformerFactory(),
		dial:     defaultDialer,
		clusters: make(map[string]*clusterEntry),
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// ErrClusterExists is returned by AddCluster when a member with the
// given name is already attached. Callers must RemoveCluster first if
// they want to reconfigure a member.
var ErrClusterExists = errors.New("cluster already attached")

// AddCluster attaches a new member cluster. It validates the name,
// builds the dynamic client from kubeconfig bytes, hands them to the
// configured InformerFactory, and starts the informer in its own
// goroutine. AddCluster returns once the informer's Start has been
// invoked; cache-sync completes asynchronously.
//
// A failure to build the client returns the error and leaves the
// Manager unchanged. A failure inside the informer's Start (post-
// return) is recorded on the entry and surfaced through Errors(); it
// never tears down sibling clusters.
func (m *Manager) AddCluster(ctx context.Context, name string, kubeconfig []byte) error {
	if err := ValidateClusterName(name); err != nil {
		return err
	}
	m.mu.Lock()
	if _, ok := m.clusters[name]; ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrClusterExists, name)
	}
	m.mu.Unlock()

	dyn, err := m.dial(kubeconfig)
	if err != nil {
		return fmt.Errorf("attach cluster %q: %w", name, err)
	}
	starter := m.factory(name, dyn, m.store)

	cctx, cancel := context.WithCancel(ctx)
	entry := &clusterEntry{name: name, cancel: cancel, done: make(chan struct{})}

	m.mu.Lock()
	// Re-check after the dial — concurrent AddCluster for the same
	// name must lose cleanly.
	if _, ok := m.clusters[name]; ok {
		m.mu.Unlock()
		cancel()
		return fmt.Errorf("%w: %s", ErrClusterExists, name)
	}
	m.clusters[name] = entry
	m.mu.Unlock()

	go func() {
		defer close(entry.done)
		if err := starter.Start(cctx); err != nil && !errors.Is(err, context.Canceled) {
			entry.err = err
			slog.Warn("multicluster: informer stopped with error",
				"cluster", name, "err", err)
		}
	}()

	slog.Info("multicluster: attached cluster", "cluster", name)
	return nil
}

// RemoveCluster cancels the named cluster's informer and waits for
// its goroutine to return. Removing an unknown cluster is a no-op.
// Returns the informer's terminal error, if any.
func (m *Manager) RemoveCluster(name string) error {
	m.mu.Lock()
	entry, ok := m.clusters[name]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	delete(m.clusters, name)
	m.mu.Unlock()

	entry.cancel()
	<-entry.done
	if entry.err != nil {
		return fmt.Errorf("cluster %q: %w", name, entry.err)
	}
	return nil
}

// ListClusters returns the attached cluster names in ascending order.
// The list is a snapshot — callers may use it without holding any
// locks.
func (m *Manager) ListClusters() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.clusters))
	for name := range m.clusters {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Errors returns a snapshot of every cluster whose informer has
// exited with a non-cancellation error. Empty (and nil) means
// everything is running normally. Useful for /readyz and ops surfaces
// that want to surface partial-degradation in the federation.
func (m *Manager) Errors() map[string]error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out map[string]error
	for name, entry := range m.clusters {
		// Non-blocking check: an entry whose done channel hasn't
		// closed is still running and has no terminal error yet.
		select {
		case <-entry.done:
			if entry.err != nil {
				if out == nil {
					out = make(map[string]error)
				}
				out[name] = entry.err
			}
		default:
		}
	}
	return out
}

// Stop cancels every attached cluster and waits for all of them to
// return. Used at shutdown.
func (m *Manager) Stop() {
	m.mu.Lock()
	entries := make([]*clusterEntry, 0, len(m.clusters))
	for _, entry := range m.clusters {
		entries = append(entries, entry)
	}
	m.clusters = make(map[string]*clusterEntry)
	m.mu.Unlock()

	for _, entry := range entries {
		entry.cancel()
	}
	for _, entry := range entries {
		<-entry.done
	}
}

// AddFromSecret attaches one cluster per key in data, using the key
// as the cluster name and the value as the kubeconfig payload. Names
// are validated with ValidateClusterName before any dial happens.
//
// A single cluster's failure logs a warning and is recorded on the
// returned map; the rest still attach. Returns nil when every member
// attached successfully.
func (m *Manager) AddFromSecret(ctx context.Context, data map[string][]byte) map[string]error {
	if len(data) == 0 {
		return nil
	}
	// Sort for deterministic startup order; nice for logs and tests.
	names := make([]string, 0, len(data))
	for name := range data {
		names = append(names, name)
	}
	sort.Strings(names)

	var failures map[string]error
	for _, name := range names {
		if err := m.AddCluster(ctx, name, data[name]); err != nil {
			slog.Warn("multicluster: skipping cluster",
				"cluster", name, "err", err)
			if failures == nil {
				failures = make(map[string]error)
			}
			failures[name] = err
		}
	}
	return failures
}
