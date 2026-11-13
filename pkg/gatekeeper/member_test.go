// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
package gatekeeper

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func memberGKSource(t *testing.T, tracker *operations.CoverageTracker, cluster string) operations.CoverageSourceSnapshot {
	t.Helper()
	s, err := tracker.SnapshotCoverage(context.Background(), cluster, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range s.Sources {
		if source.Source == operations.CoverageSourceGatekeeper {
			return source
		}
	}
	return operations.CoverageSourceSnapshot{}
}
func TestGatekeeperMemberIdentitiesEdgesDeleteAndSharedMetrics(t *testing.T) {
	tracker := operations.NewCoverageTracker(time.Minute)
	store := memory.New()
	metrics := discovery.NewDynamicMetrics()
	for _, cluster := range []string{"east", "west", ""} {
		if err := store.UpsertResource(context.Background(), graph.Resource{ClusterID: cluster, Namespace: "default", Kind: "Pod", Name: "same"}); err != nil {
			t.Fatal(err)
		}
	}
	east, eastTemplate := coverageClient()
	west, _ := coverageClient()

	for _, client := range []*dynamicfake.FakeDynamicClient{east, west} {
		constraint, err := client.Resource(coverageConstraintGVR).Get(context.Background(), "test-labels", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		constraint.Object["spec"] = map[string]any{"match": map[string]any{"kinds": []any{map[string]any{"apiGroups": []any{""}, "kinds": []any{"Pod"}}}}}
		if _, err = client.Resource(coverageConstraintGVR).Update(context.Background(), constraint, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for name, client := range map[string]*dynamicfake.FakeDynamicClient{"east": east, "west": west} {
		manager := discovery.NewDynamicInformerManager(client, discovery.WithDynamicMetrics(metrics))
		startCoverageDiscovery(t, New(client, store, extractor.Default(), manager, WithCoverage(tracker), WithClusterID(name), WithDiscovery(&fakeGKDiscovery{available: map[string]bool{gkGV(): true}})))
	}
	waitFor(t, 3*time.Second, "member constraints observed", func() bool {
		return memberGKSource(t, tracker, "east").State == operations.CoverageObserved && memberGKSource(t, tracker, "west").State == operations.CoverageObserved
	})
	if metrics.Snapshot().Active != 2 {
		t.Fatalf("shared metrics lost a member: %+v", metrics.Snapshot())
	}
	for _, name := range []string{"east", "west"} {
		id := name + ":/K8sRequiredLabels/test-labels"
		value, err := store.GetResource(context.Background(), id)
		if err != nil || value.ClusterID != name {
			t.Fatalf("constraint scope: %+v %v", value, err)
		}
		edges, err := store.ListEdges(context.Background(), id, graph.DirectionOutgoing)
		if err != nil || len(edges) != 1 || edges[0].To != name+":default/Pod/same" {
			t.Fatalf("cross-member enforcement: %+v %v", edges, err)
		}
	}
	if err := east.Resource(coverageConstraintGVR).Delete(context.Background(), "test-labels", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "scoped constraint deletion", func() bool {
		_, err := store.GetResource(context.Background(), "east:/K8sRequiredLabels/test-labels")
		return err != nil
	})
	if _, err := store.GetResource(context.Background(), "west:/K8sRequiredLabels/test-labels"); err != nil {
		t.Fatal("constraint deletion crossed member scope")
	}
	if err := east.Resource(constraintTemplateGVR).Delete(context.Background(), eastTemplate.GetName(), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "one member registration removed", func() bool { return metrics.Snapshot().Active == 1 })
	if source := memberGKSource(t, tracker, ""); source.Source != "" {
		t.Fatal("source evidence leaked to standalone scope")
	}
	for _, action := range append(east.Actions(), west.Actions()...) {
		if action.GetResource().Resource == "secrets" {
			t.Fatal("Secret objects requested")
		}
	}
}

type memberProbeExtractor struct{ foreign bool }

func (p memberProbeExtractor) ExtractAll(ctx context.Context, r graph.Resource, lister graph.ResourceLister) ([]graph.Edge, error) {
	targets, err := lister.ListResources(ctx, graph.Filter{Kind: "Pod", ClusterID: "west"})
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		if target.ClusterID != r.ClusterID {
			return nil, errors.New("extractor broadened member reads")
		}
	}
	if p.foreign {
		return []graph.Edge{{From: r.ID(), To: "west:default/Pod/same", Type: graph.EdgeTypeEnforces}}, nil
	}
	return nil, nil
}
func TestGatekeeperMemberCannotBroadenExtractorScopeOrWriteForeignEdges(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		t.Run("scope="+cluster, func(t *testing.T) {
			for _, foreign := range []bool{false, true} {
				t.Run(map[bool]string{false: "scoped reads", true: "foreign edge rejection"}[foreign], func(t *testing.T) {
					tracker := operations.NewCoverageTracker(time.Minute)
					store := memory.New()
					client, _ := coverageClient()
					for _, name := range []string{"east", "west", ""} {
						if err := store.UpsertResource(context.Background(), graph.Resource{ClusterID: name, Kind: "Pod", Namespace: "default", Name: "same"}); err != nil {
							t.Fatal(err)
						}
					}
					d := New(client, store, memberProbeExtractor{foreign}, discovery.NewDynamicInformerManager(client), WithClusterID(cluster), WithCoverage(tracker))
					startCoverageDiscovery(t, d)
					waitFor(t, 3*time.Second, "member extractor result", func() bool {
						row := coverageRow(memberGKSource(t, tracker, cluster), coverageConstraintGVR)
						if foreign {
							return slices.Contains(row.Reasons, operations.CoverageExtractionFailed)
						}
						return row.State == operations.CoverageObserved
					})
					id := graph.Resource{ClusterID: cluster, Kind: "K8sRequiredLabels", Name: "test-labels"}.ID()
					edges, err := store.ListEdges(context.Background(), id, graph.DirectionOutgoing)
					if err != nil || len(edges) != 0 {
						t.Fatal("extractor wrote foreign edges")
					}
				})
			}
		})
	}
}
func TestGatekeeperMemberShutdownJoinsBlockedConstraintCallback(t *testing.T) {
	client, _ := coverageClient()
	store := &coverageBlockingStore{GraphStore: memory.New(), entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(store.release) })
	d := New(client, store, nilRegistry{}, discovery.NewDynamicInformerManager(client), WithClusterID("east"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	select {
	case <-store.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("constraint callback missing")
	}
	// A removed registration still owns its pending callback until Run returns.
	d.dynMgr.Remove(coverageConstraintGVR)
	cancel()
	select {
	case <-done:
		t.Fatal("collector returned while graph callback remained active")
	case <-time.After(30 * time.Millisecond):
	}
	release.Do(func() { close(store.release) })
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("collector failed to join callback")
	}
}
