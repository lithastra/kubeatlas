// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type kyvernoReadStore struct {
	graph.GraphStore
	read func(context.Context, string, graph.Filter) ([]graph.Resource, error)
}

func (s *kyvernoReadStore) ListResourcesInCluster(ctx context.Context, cluster string, f graph.Filter) ([]graph.Resource, error) {
	return s.read(ctx, cluster, f)
}

var kyvernoCoverageGVR = schema.GroupVersionResource{Group: "kyverno.io", Version: "v1", Resource: "clusterpolicies"}

func kyvernoCoverageFixture(t *testing.T, cluster string, history bool) (*InformerManager, *memory.Store, *kyvernoReadStore, *resyncSink, *operations.CoverageTracker, *unstructured.Unstructured) {
	t.Helper()
	s := memory.New()
	store := &kyvernoReadStore{GraphStore: s, read: s.ListResourcesInCluster}
	registry := extractor.New()
	registry.Register(extractor.KyvernoExtractor{})
	m := NewInformerManager(nil, store, WithClusterID(cluster), WithExtractor(registry), WithMemberRepair(NewMemberRepair(nil, store, cluster)))
	tracker := operations.NewCoverageTracker(time.Minute)
	var err error
	m.coverageSession, err = tracker.Begin(cluster, []schema.GroupVersionResource{kyvernoCoverageGVR}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	sink := &resyncSink{}
	if history {
		cancellationHistory(m, store, cluster, sink)
	}
	object := resyncObject("ClusterPolicy", "policy", "policy-uid", "version")
	object.SetAPIVersion("kyverno.io/v1")
	object.SetNamespace("")
	object.Object["spec"] = map[string]any{"rules": []any{map[string]any{
		"name": "validate", "validate": map[string]any{"message": "required"},
		"match": map[string]any{"any": []any{map[string]any{"resources": map[string]any{"kinds": []any{"Pod", "Deployment"}, "namespaces": []any{"test"}}}}},
	}}}
	scopeTargets(t, s, "Pod")
	for _, owner := range []string{cluster, "west"} {
		for _, kind := range []string{"PolicyReport", "ClusterPolicyReport"} {
			result := "pass"
			if owner == "west" {
				result = "foreign-canary"
			}
			report := graph.Resource{ClusterID: owner, Kind: kind, Namespace: "test", Name: "report", GroupVersion: "wgpolicyk8s.io/v1alpha2",
				Raw: map[string]any{"results": []any{map[string]any{"policy": "policy", "result": result, "resources": []any{map[string]any{"kind": "Pod", "namespace": "test", "name": "same"}}}}}}
			if err := s.UpsertResource(context.Background(), report); err != nil {
				t.Fatal(err)
			}
		}
	}
	return m, s, store, sink, tracker, object
}

func TestCoreKyvernoReadFailureRecordsStickyExtractionGap(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, failed := range []string{"PolicyReport", "ClusterPolicyReport", "both", "Deployment"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/failed=%s", cluster, history, failed), func(t *testing.T) {
					m, s, store, sink, tracker, object := kyvernoCoverageFixture(t, cluster, history)
					var calls []string
					store.read = func(ctx context.Context, owner string, f graph.Filter) ([]graph.Resource, error) {
						if owner != cluster || f.ClusterID != cluster {
							t.Fatal("report query escaped its owning scope", owner, f)
						}
						calls = append(calls, f.Kind)
						rows, err := s.ListResourcesInCluster(ctx, owner, f)
						if f.Kind == failed || (failed == "both" && (f.Kind == "PolicyReport" || f.Kind == "ClusterPolicyReport")) {
							return rows, errors.New("injected scoped store read failure")
						}
						return rows, err
					}
					m.handleUpsert(context.Background(), kyvernoCoverageGVR, object, graph.EventTypeAdd)
					if !reflect.DeepEqual(calls, []string{"PolicyReport", "ClusterPolicyReport", "Pod", "Deployment"}) {
						t.Fatal("failed lookup retried or healthy lookup skipped", calls)
					}
					row := coverageView(t, tracker, cluster, kyvernoCoverageGVR)
					if row.ExtractionGapSince.IsZero() || !row.PersistenceGapSince.IsZero() || !row.ProcessingGapSince.IsZero() {
						t.Fatal("read failure lacks extraction-only qualification", row)
					}
					r := UnstructuredToResource(object, "ClusterPolicy")
					r.ClusterID = cluster
					edges, err := s.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
					wantResult := "pass"
					if failed == "both" {
						wantResult = ""
					}
					if err != nil || len(edges) != 1 || edges[0].To != (graph.Resource{ClusterID: cluster, Namespace: "test", Kind: "Pod", Name: "same"}).ID() || edges[0].Attributes["result"] != wantResult {
						t.Fatal("usable scoped edges were lost or contaminated", edges, err)
					}
					// A normal subsequent delivery repairs the overlay, without retrying
					// this callback or erasing evidence that an earlier read failed.
					store.read = s.ListResourcesInCluster
					m.handleUpsert(context.Background(), kyvernoCoverageGVR, object, graph.EventTypeUpdate)
					edges, err = s.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
					if err != nil || len(edges) != 1 || edges[0].Attributes["result"] != "pass" {
						t.Fatal("healthy replay did not recover the scoped report", edges, err)
					}
					after := coverageView(t, tracker, cluster, kyvernoCoverageGVR)
					if !after.ExtractionGapSince.Equal(row.ExtractionGapSince) {
						t.Fatal("healthy replay erased or moved the original gap", after)
					}
					wantEvents := 0
					if history {
						wantEvents = 1
					}
					if len(sink.recorded()) != wantEvents {
						t.Fatal("edge recovery manufactured resource history", sink.recorded())
					}
				})
			}
		}
	}
}

func TestCoreKyvernoAbsentReportsDoNotCreateFailureGap(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			t.Run(fmt.Sprintf("cluster=%s/history=%t", cluster, history), func(t *testing.T) {
				m, s, store, _, tracker, object := kyvernoCoverageFixture(t, cluster, history)
				store.read = func(ctx context.Context, owner string, f graph.Filter) ([]graph.Resource, error) {
					if f.Kind == "PolicyReport" || f.Kind == "ClusterPolicyReport" {
						return nil, nil // A successful empty query is not a read failure.
					}
					return s.ListResourcesInCluster(ctx, owner, f)
				}
				m.handleUpsert(context.Background(), kyvernoCoverageGVR, object, graph.EventTypeAdd)
				row := coverageView(t, tracker, cluster, kyvernoCoverageGVR)
				if !row.ExtractionGapSince.IsZero() || !row.PersistenceGapSince.IsZero() {
					t.Fatal("empty report lookup was misclassified as failure", row)
				}
				r := UnstructuredToResource(object, "ClusterPolicy")
				r.ClusterID = cluster
				edges, err := s.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
				if err != nil || len(edges) != 1 || edges[0].Attributes["result"] != "" {
					t.Fatal("report absence suppressed or fabricated a result", edges, err)
				}
			})
		}
	}
}

func TestCoreKyvernoCancelledReportReadDoesNotCreateFailureGap(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			t.Run(fmt.Sprintf("cluster=%s/history=%t", cluster, history), func(t *testing.T) {
				m, s, store, sink, tracker, object := kyvernoCoverageFixture(t, cluster, history)
				prior := time.Now().Add(-time.Minute)
				m.coverageSession.RecordGap(kyvernoCoverageGVR, operations.CoverageProcessingFailed, prior)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				store.read = func(_ context.Context, _ string, _ graph.Filter) ([]graph.Resource, error) {
					calls++
					cancel()
					return nil, errors.New("late dependency failure")
				}
				m.handleUpsert(ctx, kyvernoCoverageGVR, object, graph.EventTypeAdd)
				row := coverageView(t, tracker, cluster, kyvernoCoverageGVR)
				if calls != 1 || !row.ExtractionGapSince.IsZero() || !row.PersistenceGapSince.IsZero() || !row.ProcessingGapSince.Equal(prior) {
					t.Fatal("cancellation continued reads or changed failure evidence", calls, row)
				}
				r := UnstructuredToResource(object, "ClusterPolicy")
				r.ClusterID = cluster
				if _, err := s.GetResource(context.Background(), r.ID()); err != nil {
					t.Fatal("cancellation lost the committed resource", err)
				}
				edges, err := s.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
				if err != nil || len(edges) != 0 {
					t.Fatal("cancelled extraction persisted edges", edges, err)
				}
				wantEvents := 0
				if history {
					wantEvents = 1
				}
				if len(sink.recorded()) != wantEvents {
					t.Fatal("cancellation lost committed history", sink.recorded())
				}
			})
		}
	}
}
