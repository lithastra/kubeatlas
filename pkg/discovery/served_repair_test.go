// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

type servedRepairClient struct {
	dynamic.Interface
	list func(context.Context, schema.GroupVersionResource, metav1.ListOptions) (*unstructured.UnstructuredList, error)
}

func (c *servedRepairClient) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &coverageStubResource{list: func(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
		return c.list(ctx, gvr, opts)
	}}
}

var servedRepairGVR = schema.GroupVersionResource{Group: "example.com", Version: "v2", Resource: "foos"}

func servedRepairRow(cluster, version, name string) graph.Resource {
	return graph.Resource{ClusterID: cluster, Namespace: "default", Kind: "Foo", GroupVersion: "example.com/" + version, Name: name, UID: types.UID("uid-" + name), ResourceVersion: "opaque-rv"}
}

func TestServedRepairUsesEachVersionsOwnSnapshot(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		t.Run("scope="+cluster, func(t *testing.T) {
			ctx := context.Background()
			s := memory.New()
			oldLive := servedRepairRow(cluster, "v1", "live-only-in-old-list")
			oldGone := servedRepairRow(cluster, "v1", "old-gone")
			currentGone := servedRepairRow(cluster, "v2", "current-gone")
			unserved := servedRepairRow(cluster, "v0", "unserved")
			foreign := oldGone
			foreign.ClusterID = "west"
			for _, row := range []graph.Resource{oldLive, oldGone, currentGone, unserved, foreign} {
				if err := s.UpsertResource(ctx, row); err != nil {
					t.Fatal(err)
				}
			}
			var calls []string
			c := &servedRepairClient{list: func(_ context.Context, gvr schema.GroupVersionResource, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
				calls = append(calls, gvr.Version)
				if gvr.Group != servedRepairGVR.Group || gvr.Resource != servedRepairGVR.Resource || opts.ResourceVersion != "" || opts.Limit != memberRepairPageSize || opts.LabelSelector != "" || opts.FieldSelector != "" {
					t.Fatal("unsafe endpoint request", gvr, opts)
				}
				if gvr.Version == "v1" {
					page := repairPage(oldLive)
					page.SetResourceVersion("independent-v1-snapshot")
					return page, nil
				}
				return repairPage(), nil
			}}
			r := NewMemberRepair(c, s, cluster)
			if cluster == "" {
				r = NewStandaloneRepair(c, s)
			}
			sink := &resyncSink{}
			history := NewResourceHistory(s, cluster, sink)
			if cluster == "" {
				history = NewStandaloneResourceHistory(s, sink)
			}
			for range 2 {
				r.repairVersionsOnce(ctx, servedRepairGVR, "Foo", []string{"v2", "v1"}, nil, history.RecordDelete)
			}
			for _, row := range []graph.Resource{oldLive, unserved, foreign} {
				if _, err := s.GetResource(ctx, row.ID()); err != nil {
					t.Fatal("new-version absence swept a protected row", row, err)
				}
			}
			for _, row := range []graph.Resource{oldGone, currentGone} {
				if _, err := s.GetResource(ctx, row.ID()); !errors.As(err, new(graph.ErrNotFound)) {
					t.Fatal("own-endpoint absence did not repair", row, err)
				}
			}
			if !reflect.DeepEqual(calls, []string{"v2", "v1", "v1"}) {
				t.Fatal("queried unserved or candidate-free endpoint", calls)
			}
			events := sink.recorded()
			if len(events) != 2 {
				t.Fatal("committed deletes duplicated or missing", events)
			}
			for _, event := range events {
				if event.ClusterID != cluster || event.EventType != graph.EventTypeDelete || event.Data != nil {
					t.Fatal("unscoped or payload-bearing repair event", event)
				}
			}
		})
	}
}

func TestServedRepairSharesBudgetsAndDefersAllDeletesUntilValidated(t *testing.T) {
	for _, scenario := range []string{"old-api-missing", "old-api-denied", "partial-old-page", "invalid-old-item", "total-items", "total-pages", "total-candidates", "receipt-changed"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			s := memory.New()
			old, current := servedRepairRow("east", "v1", "old"), servedRepairRow("east", "v2", "current")
			for _, row := range []graph.Resource{old, current} {
				if err := s.UpsertResource(ctx, row); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "total-candidates" {
				for i := 0; i < memberRepairLimit-1; i++ {
					if err := s.UpsertResource(ctx, servedRepairRow("east", "v1", fmt.Sprintf("extra-%d", i))); err != nil {
						t.Fatal(err)
					}
				}
			}
			calls := 0
			c := &servedRepairClient{list: func(_ context.Context, gvr schema.GroupVersionResource, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
				calls++
				page := repairPage()
				if scenario == "total-items" {
					for i := 0; i < 6000; i++ {
						page.Items = append(page.Items, repairPage(servedRepairRow("east", gvr.Version, fmt.Sprintf("live-%d", i))).Items[0])
					}
					return page, nil
				}
				if scenario == "total-pages" {
					// Consume 31 pages on v2; v1 then needs two pages. The
					// 33rd request must never be made and no row removed.
					if gvr.Version == "v2" && calls < 31 || gvr.Version == "v1" {
						page.SetContinue(fmt.Sprintf("page-%d", calls))
					}
					return page, nil
				}
				if gvr.Version == "v2" {
					return page, nil
				}
				switch scenario {
				case "old-api-missing", "old-api-denied":
					return nil, errors.New(scenario)
				case "partial-old-page":
					if opts.Continue != "" {
						return nil, errors.New("expired old-version page")
					}
					page.SetContinue("old-next")
				case "invalid-old-item":
					page = repairPage(current) // Wrong API version cannot prove old absence.
				case "receipt-changed":
					old.GroupVersion, old.ResourceVersion = "example.com/v2", "replacement-rv"
					if err := s.UpsertResource(ctx, old); err != nil {
						t.Fatal(err)
					}
				}
				return page, nil
			}}
			tracker := operations.NewCoverageTracker(time.Minute)
			coverage, err := tracker.Begin("east", []schema.GroupVersionResource{servedRepairGVR}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			deleted := 0
			NewMemberRepair(c, s, "east").repairVersionsOnce(ctx, servedRepairGVR, "Foo", []string{"v2", "v1"}, coverage, func(graph.Resource) { deleted++ })
			got, err := s.GetResource(ctx, old.ID())
			if err != nil || !graph.SameResourceVersion(got, old) {
				t.Fatal("unsafe old-version removal", got, err)
			}
			if scenario == "receipt-changed" {
				if deleted != 1 {
					t.Fatal("unchanged current receipt not repaired", deleted)
				}
				return
			}
			if deleted != 0 {
				t.Fatal("failed later endpoint authorized an earlier deletion")
			}
			if _, err := s.GetResource(ctx, current.ID()); err != nil {
				t.Fatal("current row swept before validating all endpoints")
			}
			if scenario == "total-pages" && calls != 32 || scenario == "total-candidates" && calls != 0 {
				t.Fatal("aggregate budget not enforced", calls)
			}
			evidence, err := tracker.SnapshotCoverage(ctx, "east", []schema.GroupVersionResource{servedRepairGVR})
			if err != nil || len(evidence.Resources) != 1 || evidence.Resources[0].ProcessingGapSince.IsZero() {
				t.Fatal("failed auxiliary list hid its coverage gap", evidence, err)
			}
		})
	}
}

func TestServedRepairRejectsInvalidVersionSetsBeforeRequests(t *testing.T) {
	for _, versions := range [][]string{nil, {"v1"}, {"v2", "v2"}, {"v2", "v1/evil"}, {"v2", ""}, make([]string, 33)} {
		t.Run(fmt.Sprint(versions), func(t *testing.T) {
			c := &servedRepairClient{list: func(context.Context, schema.GroupVersionResource, metav1.ListOptions) (*unstructured.UnstructuredList, error) {
				t.Fatal("invalid configuration made a request")
				return nil, nil
			}}
			NewStandaloneRepair(c, memory.New()).repairVersionsOnce(context.Background(), servedRepairGVR, "Foo", versions, nil, nil)
		})
	}
}

func TestServedRepairPreservesRetiredScopesAndRejectsWrongScopeLists(t *testing.T) {
	for _, namespaced := range []bool{false, true} {
		for _, badList := range []bool{false, true} {
			t.Run(fmt.Sprintf("namespaced=%t/bad-list=%t", namespaced, badList), func(t *testing.T) {
				ctx := context.Background()
				s := memory.New()
				active, retired := servedRepairRow("east", "v1", "active"), servedRepairRow("east", "v1", "retired-scope")
				if namespaced {
					retired.Namespace = ""
				} else {
					active.Namespace = ""
				}
				for _, row := range []graph.Resource{active, retired} {
					if err := s.UpsertResource(ctx, row); err != nil {
						t.Fatal(err)
					}
				}
				c := &servedRepairClient{list: func(context.Context, schema.GroupVersionResource, metav1.ListOptions) (*unstructured.UnstructuredList, error) {
					if badList {
						return repairPage(retired), nil
					}
					return repairPage(), nil
				}}
				NewMemberRepair(c, s, "east").repairScopedVersionsOnce(ctx, servedRepairGVR, "Foo", []string{"v2", "v1"}, &namespaced, nil, nil)
				if _, err := s.GetResource(ctx, retired.ID()); err != nil {
					t.Fatal("current endpoint absence swept a retired scope", err)
				}
				_, err := s.GetResource(ctx, active.ID())
				if badList && err != nil || !badList && !errors.As(err, new(graph.ErrNotFound)) {
					t.Fatal("scope validation did not control deletion", err)
				}
			})
		}
	}
}

func TestServedRepairRevokedRoundUsesNewSetOnNextScheduledRound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	epoch, revoke := context.WithCancel(ctx)
	defer revoke()
	s := memory.New()
	old, current := servedRepairRow("east", "v1", "old"), servedRepairRow("east", "v2", "current")
	for _, row := range []graph.Resource{old, current} {
		if err := s.UpsertResource(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var changed atomic.Bool
	var oldLists atomic.Int64
	c := &servedRepairClient{list: func(_ context.Context, gvr schema.GroupVersionResource, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
		if gvr.Version == "v1" {
			if oldLists.Add(1) == 1 {
				close(entered)
			}
			<-release
		}
		return repairPage(), nil
	}}
	r := NewMemberRepair(c, s, "east")
	r.interval = time.Millisecond // Test-only clock; production stays five minutes.
	deleted := make(chan graph.Resource, 2)
	go func() {
		defer close(done)
		r.RunServedVersions(ctx, servedRepairGVR, "Foo", nil, func() bool { return true }, func(row graph.Resource) { deleted <- row }, func() ServedRepairVersions {
			if changed.Load() {
				return ServedRepairVersions{Versions: []string{"v2"}, Context: ctx}
			}
			return ServedRepairVersions{Versions: []string{"v1", "v2"}, Context: epoch}
		})
	}()
	var released atomic.Bool
	unblock := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	t.Cleanup(func() {
		cancel()
		unblock()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("repair worker did not join")
		}
	})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("old-version round did not begin")
	}
	changed.Store(true)
	revoke()
	unblock()
	select {
	case row := <-deleted:
		if row.ID() != current.ID() {
			t.Fatal("revoked round removed an old-version row", row)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("next scheduled round did not use the new set")
	}
	cancel()
	<-done
	if oldLists.Load() != 1 {
		t.Fatal("revoked endpoint requested again", oldLists.Load())
	}
	if _, err := s.GetResource(context.Background(), old.ID()); err != nil {
		t.Fatal("unserved version swept by selected-version list", err)
	}
}

func TestServedRepairWithoutClientKeepsRetainedRows(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		t.Run("scope="+cluster, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			store := memory.New()
			row := servedRepairRow(cluster, "v2", "retained")
			if err := store.UpsertResource(ctx, row); err != nil {
				t.Fatal(err)
			}
			repair := NewMemberRepair(nil, store, cluster)
			if cluster == "" {
				repair = NewStandaloneRepair(nil, store)
			}
			syncCalls, versionCalls := 0, 0
			repair.RunServedVersions(ctx, servedRepairGVR, "Foo", nil, func() bool {
				syncCalls++
				return true
			}, func(graph.Resource) {
				t.Error("lock-only repair reported a deletion")
			}, func() ServedRepairVersions {
				versionCalls++
				return ServedRepairVersions{Versions: []string{"v2"}}
			})
			if syncCalls != 0 || versionCalls != 0 {
				t.Fatal("lock-only repair started a reconciliation round")
			}
			unlock, err := repair.LockResource(ctx, row.ID())
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			got, err := store.GetResource(ctx, row.ID())
			if err != nil || got.UID != row.UID || got.ResourceVersion != row.ResourceVersion {
				t.Fatal("lock-only repair lost the retained row", got, err)
			}
		})
	}
}
