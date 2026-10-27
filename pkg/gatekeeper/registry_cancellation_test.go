// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package gatekeeper

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

type constraintRegistryStep func(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error)

func (constraintRegistryStep) Type() graph.EdgeType { return graph.EdgeTypeEnforces }
func (f constraintRegistryStep) Extract(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
	return f(ctx, r, q)
}

func TestConstraintRegistryCancellationPreservesCommitAndReplay(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, partial := range []bool{false, true} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/partial=%t", cluster, history, partial), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					store, reg := memory.New(), extractor.New()
					tracker := operations.NewCoverageTracker(time.Minute)
					source, err := tracker.BeginSource(cluster, operations.CoverageSourceGatekeeper, time.Now())
					if err != nil {
						t.Fatal(err)
					}
					token, err := source.BeginType(coverageConstraintGVR, time.Now())
					if err != nil || !token.BindOrigin(graph.ResourceOrigin{OwnerName: "k8srequiredlabels", OwnerUID: "template-uid", Group: constraintGroup, Version: constraintVersion, Resource: coverageConstraintGVR.Resource, Kind: "K8sRequiredLabels"}) {
						t.Fatal("origin binding failed", err)
					}
					prior := time.Now().Add(-time.Minute)
					token.RecordGap(coverageConstraintGVR, operations.CoverageExtractionFailed, prior)
					_, obj := coverageObjects()
					r := discovery.UnstructuredToResource(obj, obj.GetKind())
					r.ClusterID = cluster
					r = token.StampOrigin(r)
					active, laterReads := true, 0
					for index := range 2 {
						reg.Register(constraintRegistryStep(func(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
							if active && index == 1 {
								cancel()
							}
							var err error
							if active && partial {
								err = errors.New("partial constraint failure")
							}
							target := graph.Resource{ClusterID: cluster, Namespace: "default", Kind: "Pod", Name: fmt.Sprintf("target%d", index)}
							return []graph.Edge{{From: r.ID(), To: target.ID(), Type: graph.EdgeTypeEnforces}}, err
						}))
					}
					reg.Register(constraintRegistryStep(func(ctx context.Context, _ graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
						laterReads++
						rows, err := q.ListResources(ctx, graph.Filter{Kind: r.Kind, ClusterID: "west"})
						if err != nil || len(rows) != 1 || rows[0].ClusterID != cluster {
							t.Error("recovered constraint registry escaped scope", rows, err)
						}
						return nil, err
					}))
					sink := &dynamicHistorySink{}
					collector := New(nil, store, reg, nil, WithClusterID(cluster), WithMemberRepair(discovery.NewMemberRepair(nil, store, cluster)))
					if history {
						collector.resourceHistory = discovery.NewResourceHistory(store, cluster, sink)
						if cluster == "" {
							collector.resourceHistory = discovery.NewStandaloneResourceHistory(store, sink)
						}
					}
					collector.handleConstraintEvent(ctx, r.Kind, obj, token, graph.EventTypeAdd)
					if ctx.Err() == nil || laterReads != 0 {
						t.Fatal("cancelled constraint began later reads", laterReads)
					}
					stored, err := store.GetResource(context.Background(), r.ID())
					if err != nil || !stored.Origin.Matches(stored) || !graph.SameResourceVersion(stored, r) {
						t.Fatal("cancelled constraint lost committed writer receipt", stored, err)
					}
					edges, err := store.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
					wantEvents := 0
					if history {
						wantEvents = 1
					}
					if err != nil || len(edges) != 0 || len(sink.events) != wantEvents {
						t.Fatal("cancelled registry changed committed data/history", edges, sink.events, err)
					}
					before := coverageRow(memberGKSource(t, tracker, cluster), coverageConstraintGVR)
					if !before.ExtractionGapSince.Equal(prior) || !before.PersistenceGapSince.IsZero() || !before.ProcessingGapSince.IsZero() {
						t.Fatal("constraint teardown introduced loss", before)
					}
					active = false
					for range 2 {
						collector.handleConstraintEvent(context.Background(), r.Kind, obj, token, graph.EventTypeUpdate)
					}
					edges, err = store.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
					if err != nil || len(edges) != 2 || laterReads != 2 || len(sink.events) != wantEvents {
						t.Fatal("constraint replay lost edges or duplicated history", edges, laterReads, sink.events, err)
					}
					after := coverageRow(memberGKSource(t, tracker, cluster), coverageConstraintGVR)
					if !after.ExtractionGapSince.Equal(prior) || !after.PersistenceGapSince.IsZero() || !after.ProcessingGapSince.IsZero() {
						t.Fatal("constraint replay changed loss evidence", after)
					}
					retained, err := store.GetResource(context.Background(), r.ID())
					if err != nil || retained.Origin != stored.Origin {
						t.Fatal("edge recovery adopted a different writer receipt", retained, err)
					}
				})
			}
		}
	}
}
