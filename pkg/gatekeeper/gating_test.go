// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package gatekeeper

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sdiscovery "k8s.io/client-go/discovery"
)

// fakeGKDiscovery answers ServerResourcesForGroupVersion from a fixed set
// of available GroupVersions; any other GV returns NotFound, mirroring a
// real apiserver when the group is uninstalled. available is guarded so a
// test can flip it while awaitConstraintTemplateCRD polls.
type fakeGKDiscovery struct {
	k8sdiscovery.DiscoveryInterface
	mu        sync.Mutex
	available map[string]bool
}

func (f *fakeGKDiscovery) set(gv string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.available[gv] = ok
}

func (f *fakeGKDiscovery) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.available[gv] {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: gv, Resource: "groupversions"}, gv)
	}
	return &metav1.APIResourceList{GroupVersion: gv}, nil
}

func gkGV() string { return constraintTemplateGVR.GroupVersion().String() }

func newAwaitDiscovery(dc k8sdiscovery.DiscoveryInterface, poll time.Duration) *Discovery {
	return &Discovery{
		disco:     dc,
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		pollEvery: poll,
	}
}

func TestGatekeeperInstalled(t *testing.T) {
	d := New(nil, nil, nil, nil, WithDiscovery(
		&fakeGKDiscovery{available: map[string]bool{gkGV(): true}}))
	if ok, err := d.gatekeeperInstalled(context.Background()); err != nil || !ok {
		t.Fatalf("CRD present: got (%v, %v), want (true, nil)", ok, err)
	}

	d.disco = &fakeGKDiscovery{available: map[string]bool{}}
	if ok, err := d.gatekeeperInstalled(context.Background()); err != nil || ok {
		t.Fatalf("CRD absent: got (%v, %v), want (false, nil)", ok, err)
	}

	// No discovery client wired -> assume installed (pre-gating behaviour).
	d.disco = nil
	if ok, err := d.gatekeeperInstalled(context.Background()); err != nil || !ok {
		t.Fatalf("nil discovery: got (%v, %v), want (true, nil)", ok, err)
	}
}

func TestAwaitConstraintTemplateCRD_PresentReturnsImmediately(t *testing.T) {
	d := newAwaitDiscovery(&fakeGKDiscovery{available: map[string]bool{gkGV(): true}}, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.awaitConstraintTemplateCRD(ctx); err != nil {
		t.Fatalf("CRD present: want nil, got %v", err)
	}
}

func TestAwaitConstraintTemplateCRD_AbsentBlocksUntilCancel(t *testing.T) {
	// pollEvery is long so the only wakeup in the window is the cancel.
	d := newAwaitDiscovery(&fakeGKDiscovery{available: map[string]bool{}}, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.awaitConstraintTemplateCRD(ctx) }()

	select {
	case err := <-done:
		t.Fatalf("returned while CRD absent: %v (should block)", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want context error after cancel, got nil")
		}
	case <-time.After(time.Second):
		t.Fatal("did not return after context cancel")
	}
}

func TestAwaitConstraintTemplateCRD_PicksUpLaterInstall(t *testing.T) {
	fake := &fakeGKDiscovery{available: map[string]bool{}}
	d := newAwaitDiscovery(fake, 5*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- d.awaitConstraintTemplateCRD(ctx) }()

	// Absent at first; install Gatekeeper mid-flight — the next poll must
	// notice and unblock.
	time.Sleep(20 * time.Millisecond)
	fake.set(gkGV(), true)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("want nil once the CRD appears, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("did not detect the ConstraintTemplate CRD after it was installed")
	}
}
