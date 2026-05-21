// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package multicluster

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/dynamic"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestValidateClusterName(t *testing.T) {
	for _, c := range []struct {
		name    string
		ok      bool
		wantSub string
	}{
		{name: "prod", ok: true},
		{name: "prod-eu-west-1", ok: true},
		{name: "staging.aks", ok: true},
		{name: "", ok: false, wantSub: "empty"},
		{name: "has:colon", ok: false, wantSub: "':'"},
		{name: "has/slash", ok: false, wantSub: "'/'"},
	} {
		err := ValidateClusterName(c.name)
		if c.ok {
			if err != nil {
				t.Errorf("%q: unexpected error %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantSub) {
			t.Errorf("%q: want error containing %q, got %v", c.name, c.wantSub, err)
		}
	}
}

// fakeStarter records that Start was invoked and blocks until ctx is
// cancelled. The default error is nil; tests that need a failure
// surface set startErr.
type fakeStarter struct {
	cluster  string
	started  atomic.Bool
	startErr error
}

func (f *fakeStarter) Start(ctx context.Context) error {
	f.started.Store(true)
	<-ctx.Done()
	if f.startErr != nil {
		return f.startErr
	}
	return ctx.Err()
}

func newTestManager(t *testing.T) (*Manager, *sync.Map) {
	t.Helper()
	store := memory.New()
	var starters sync.Map // cluster name -> *fakeStarter
	factory := func(clusterName string, _ dynamic.Interface, _ graph.GraphStore) InformerStarter {
		s := &fakeStarter{cluster: clusterName}
		starters.Store(clusterName, s)
		return s
	}
	dial := func(_ []byte) (dynamic.Interface, error) { return nil, nil }
	m := New(store, WithFactory(factory), withDialer(dial))
	return m, &starters
}

func TestManager_AddCluster_StartsInformer(t *testing.T) {
	m, starters := newTestManager(t)
	defer m.Stop()

	if err := m.AddCluster(context.Background(), "prod", []byte("kubeconfig:dummy")); err != nil {
		t.Fatalf("AddCluster: %v", err)
	}
	// The factory should have been invoked with the cluster name.
	s, ok := starters.Load("prod")
	if !ok {
		t.Fatal("factory was never called for 'prod'")
	}
	// Start runs in a goroutine; wait briefly for it to flip the flag.
	if !waitFor(t, func() bool { return s.(*fakeStarter).started.Load() }) {
		t.Fatal("starter.Start was not invoked")
	}
	if got := m.ListClusters(); !reflect.DeepEqual(got, []string{"prod"}) {
		t.Errorf("ListClusters = %v, want [prod]", got)
	}
}

func TestManager_AddCluster_DuplicateRejected(t *testing.T) {
	m, _ := newTestManager(t)
	defer m.Stop()

	if err := m.AddCluster(context.Background(), "prod", nil); err != nil {
		t.Fatalf("first AddCluster: %v", err)
	}
	err := m.AddCluster(context.Background(), "prod", nil)
	if !errors.Is(err, ErrClusterExists) {
		t.Errorf("duplicate add: want ErrClusterExists, got %v", err)
	}
}

func TestManager_AddCluster_InvalidNameRejected(t *testing.T) {
	m, _ := newTestManager(t)
	defer m.Stop()
	err := m.AddCluster(context.Background(), "bad:name", nil)
	if err == nil || !strings.Contains(err.Error(), "':'") {
		t.Errorf("want validation error, got %v", err)
	}
	if got := m.ListClusters(); len(got) != 0 {
		t.Errorf("invalid add still left clusters: %v", got)
	}
}

func TestManager_AddCluster_DialerErrorBubbles(t *testing.T) {
	store := memory.New()
	dialErr := errors.New("boom")
	m := New(store,
		WithFactory(func(string, dynamic.Interface, graph.GraphStore) InformerStarter {
			return &fakeStarter{}
		}),
		withDialer(func([]byte) (dynamic.Interface, error) { return nil, dialErr }),
	)
	err := m.AddCluster(context.Background(), "prod", nil)
	if !errors.Is(err, dialErr) {
		t.Errorf("dialer error not surfaced: %v", err)
	}
	if got := m.ListClusters(); len(got) != 0 {
		t.Errorf("failed dial left state behind: %v", got)
	}
}

func TestManager_RemoveCluster_CancelsAndCleansUp(t *testing.T) {
	m, starters := newTestManager(t)
	defer m.Stop()

	if err := m.AddCluster(context.Background(), "prod", nil); err != nil {
		t.Fatalf("AddCluster: %v", err)
	}
	s, _ := starters.Load("prod")
	if !waitFor(t, func() bool { return s.(*fakeStarter).started.Load() }) {
		t.Fatal("starter never started")
	}
	if err := m.RemoveCluster("prod"); err != nil {
		t.Fatalf("RemoveCluster: %v", err)
	}
	if got := m.ListClusters(); len(got) != 0 {
		t.Errorf("ListClusters after remove = %v, want empty", got)
	}
	// Removing an unknown cluster is a no-op.
	if err := m.RemoveCluster("unknown"); err != nil {
		t.Errorf("removing unknown: %v", err)
	}
}

func TestManager_AddFromSecret_AttachesEverySuccessfulCluster(t *testing.T) {
	store := memory.New()
	var seen sync.Map
	factory := func(name string, _ dynamic.Interface, _ graph.GraphStore) InformerStarter {
		s := &fakeStarter{cluster: name}
		seen.Store(name, s)
		return s
	}
	dialErr := errors.New("bad kubeconfig")
	dial := func(b []byte) (dynamic.Interface, error) {
		if string(b) == "broken" {
			return nil, dialErr
		}
		return nil, nil
	}
	m := New(store, WithFactory(factory), withDialer(dial))
	defer m.Stop()

	failures := m.AddFromSecret(context.Background(), map[string][]byte{
		"prod":    []byte("ok"),
		"staging": []byte("broken"),
		"dev":     []byte("ok"),
	})
	if len(failures) != 1 || !errors.Is(failures["staging"], dialErr) {
		t.Errorf("failures = %v, want {staging: bad kubeconfig}", failures)
	}
	want := []string{"dev", "prod"}
	if got := m.ListClusters(); !reflect.DeepEqual(got, want) {
		t.Errorf("ListClusters = %v, want %v", got, want)
	}
}

func TestManager_Errors_ReportsTerminatedInformers(t *testing.T) {
	store := memory.New()
	wantErr := errors.New("informer crashed")
	factory := func(name string, _ dynamic.Interface, _ graph.GraphStore) InformerStarter {
		if name == "broken" {
			return &fakeStarter{startErr: wantErr}
		}
		return &fakeStarter{}
	}
	m := New(store,
		WithFactory(factory),
		withDialer(func([]byte) (dynamic.Interface, error) { return nil, nil }),
	)
	defer m.Stop()

	_ = m.AddCluster(context.Background(), "ok", nil)
	_ = m.AddCluster(context.Background(), "broken", nil)
	// Cancel just the broken one so its Start returns startErr.
	if err := m.RemoveCluster("broken"); !errors.Is(err, wantErr) {
		t.Errorf("RemoveCluster broken: got %v, want %v", err, wantErr)
	}
	// 'ok' is still running; Errors should be empty.
	if got := m.Errors(); len(got) != 0 {
		t.Errorf("Errors with no terminated clusters = %v", got)
	}
}

func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
