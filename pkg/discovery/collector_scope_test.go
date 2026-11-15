// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func scopedCoreFixture(t *testing.T, cluster string, history bool, ext ExtractorRegistry) (*InformerManager, *memory.Store, *resyncSink, *operations.CoverageTracker) {
	t.Helper()
	s := memory.New()
	sink := &resyncSink{}
	tracker := operations.NewCoverageTracker(time.Minute)
	m := NewInformerManager(nil, s, WithClusterID(cluster), WithMemberRepair(NewMemberRepair(nil, s, cluster)), WithExtractor(ext))
	var err error
	m.coverageSession, err = tracker.Begin(cluster, []schema.GroupVersionResource{configMapGVR}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if history {
		cancellationHistory(m, s, cluster, sink)
	}
	return m, s, sink, tracker
}

func scopeTargets(t *testing.T, s graph.GraphStore, kind string) map[string]graph.Resource {
	t.Helper()
	out := map[string]graph.Resource{}
	for _, cluster := range []string{"east", "west", ""} {
		r := graph.Resource{ClusterID: cluster, Kind: kind, Namespace: "test", Name: "same", Labels: map[string]string{"app": "selected"}}
		if err := s.UpsertResource(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		out[cluster] = r
	}
	return out
}

func TestCoreExtractorReadScopeCannotBeBroadened(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			t.Run(fmt.Sprintf("cluster=%s/history=%t", cluster, history), func(t *testing.T) {
				m, s, _, _ := scopedCoreFixture(t, cluster, history, nil)
				targets := scopeTargets(t, s, "Pod")
				calls := 0
				m.extractor = resyncExtractorFunc(func(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
					if _, exposed := q.(graph.GraphStore); exposed {
						t.Error("extractor received mutation/global-store capability")
					}
					for _, requested := range []string{"", "west", cluster} {
						filter := graph.Filter{Kind: "Pod", Namespace: "test", ClusterID: requested, Labels: map[string]string{"app": "selected"}}
						rows, err := q.ListResources(ctx, filter)
						if err != nil || len(rows) != 1 || rows[0].ID() != targets[cluster].ID() || filter.ClusterID != requested {
							t.Errorf("extractor query escaped its owner: %+v %v", rows, err)
						}
						calls++
					}
					return nil, nil
				})
				m.handleUpsert(context.Background(), configMapGVR, resyncObject("ConfigMap", "root", "uid", "version"), graph.EventTypeAdd)
				if calls != 3 {
					t.Fatal("extractor did not exercise every query override")
				}
			})
		}
	}
}

func TestCoreBuiltinExtractionDoesNotUseOtherMemberTargetsOrReports(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, kind := range []string{"Service", "ClusterPolicy"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/kind=%s", cluster, history, kind), func(t *testing.T) {
					registry := &extractor.Registry{}
					registry.Register(extractor.SelectsExtractor{})
					registry.Register(extractor.KyvernoExtractor{})
					m, s, sink, _ := scopedCoreFixture(t, cluster, history, registry)
					targets := scopeTargets(t, s, "Pod")
					object := resyncObject(kind, "policy", "uid", "version")
					gvr := schema.GroupVersionResource{Version: "v1", Resource: "services"}
					if kind == "Service" {
						object.Object["spec"] = map[string]any{"selector": map[string]any{"app": "selected"}}
					} else {
						object.SetNamespace("")
						object.SetAPIVersion("kyverno.io/v1")
						gvr = schema.GroupVersionResource{Group: "kyverno.io", Version: "v1", Resource: "clusterpolicies"}
						object.Object["spec"] = map[string]any{"rules": []any{map[string]any{
							"name": "validate", "validate": map[string]any{"message": "required"},
							"match": map[string]any{"any": []any{map[string]any{"resources": map[string]any{"kinds": []any{"Pod"}, "namespaces": []any{"test"}}}}},
						}}}
					}
					report := graph.Resource{ClusterID: "west", Kind: "PolicyReport", Namespace: "test", Name: "report", GroupVersion: "wgpolicyk8s.io/v1alpha2",
						Raw: map[string]any{"results": []any{map[string]any{"policy": "policy", "result": "foreign-canary", "resources": []any{map[string]any{"kind": "Pod", "namespace": "test", "name": "same"}}}}}}
					if err := s.UpsertResource(context.Background(), report); err != nil {
						t.Fatal(err)
					}
					m.handleUpsert(context.Background(), gvr, object, graph.EventTypeAdd)
					r := UnstructuredToResource(object, kind)
					r.ClusterID = cluster
					edges, err := s.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
					if err != nil || len(edges) != 1 || edges[0].To != targets[cluster].ID() || edges[0].Attributes["result"] != "" {
						t.Fatalf("builtin extraction incorporated another member: %+v %v", edges, err)
					}
					if kind == "ClusterPolicy" {
						report.ClusterID = cluster
						report.Raw = map[string]any{"results": []any{map[string]any{"policy": "policy", "result": "pass", "resources": []any{map[string]any{"kind": "Pod", "namespace": "test", "name": "same"}}}}}
						if err := s.UpsertResource(context.Background(), report); err != nil {
							t.Fatal(err)
						}
						m.handleUpsert(context.Background(), gvr, object, graph.EventTypeUpdate)
						edges, err = s.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
						if err != nil || len(edges) != 1 || edges[0].Attributes["result"] != "pass" {
							t.Fatal("scoped report replay did not recover its result", edges, err)
						}
					}
					wantEvents := 0
					if history {
						wantEvents = 1
					}
					if len(sink.recorded()) != wantEvents {
						t.Fatal("scoped replay manufactured resource history", sink.recorded())
					}
				})
			}
		}
	}
}

func TestCoreCollectorRejectsWholeForeignOrInvalidEdgeBatch(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, partial := range []bool{false, true} {
				for _, invalid := range []string{"from", "to", "empty"} {
					t.Run(fmt.Sprintf("cluster=%s/history=%t/partial=%t/invalid=%s", cluster, history, partial, invalid), func(t *testing.T) {
						m, s, sink, tracker := scopedCoreFixture(t, cluster, history, nil)
						m.extractor = resyncExtractorFunc(func(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
							edges := []graph.Edge{
								{From: r.Namespace + "/" + r.Kind + "/" + r.Name, To: "test/Secret/local", Type: graph.EdgeTypeUsesSecret},
								{From: r.ID(), To: "test/Secret/second", Type: graph.EdgeTypeUsesSecret},
							}
							switch invalid {
							case "from":
								edges[1].From = "west:test/Pod/foreign"
							case "to":
								edges[1].To = "west:test/Secret/foreign"
							case "empty":
								edges[1].To = ""
							}
							if partial {
								return edges, errors.New("partial extraction")
							}
							return edges, nil
						})
						m.handleUpsert(context.Background(), configMapGVR, resyncObject("ConfigMap", "root", "uid", "version"), graph.EventTypeAdd)
						snapshot, err := s.Snapshot(context.Background())
						if err != nil || len(snapshot.Resources) != 1 || len(snapshot.Edges) != 0 {
							t.Fatal("invalid batch persisted a partial edge/Secret placeholder", snapshot, err)
						}
						capture, err := tracker.SnapshotCoverage(context.Background(), cluster, []schema.GroupVersionResource{configMapGVR})
						if err != nil || capture.Resources[0].ExtractionGapSince.IsZero() || !capture.Resources[0].PersistenceGapSince.IsZero() {
							t.Fatal("scope rejection lost extraction qualification", capture, err)
						}
						wantEvents := 0
						if history {
							wantEvents = 1 // The root resource committed before extraction.
						}
						if len(sink.recorded()) != wantEvents {
							t.Fatal("scope rejection changed committed resource history", sink.recorded())
						}
					})
				}
			}
		}
	}
}
