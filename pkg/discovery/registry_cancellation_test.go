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
)

type coreRegistryStep func(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error)

func (coreRegistryStep) Type() graph.EdgeType { return graph.EdgeTypeUsesSecret }
func (f coreRegistryStep) Extract(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
	return f(ctx, r, q)
}

func TestCoreRegistryCancellationPreservesCommitAndReplay(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, partial := range []bool{false, true} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/partial=%t", cluster, history, partial), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					reg := extractor.New()
					m, store, sink, tracker := scopedCoreFixture(t, cluster, history, reg)
					m.snapshotSink = sink
					targets := scopeTargets(t, store, "Pod")
					prior := time.Now().Add(-time.Minute)
					m.coverageSession.RecordGap(configMapGVR, operations.CoverageExtractionFailed, prior)
					active, laterReads := true, 0
					for index := range 2 {
						reg.Register(coreRegistryStep(func(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
							if active && index == 1 {
								cancel()
							}
							var err error
							if active && partial {
								err = errors.New("partial dependency failure")
							}
							return cancellationEdges(r)[index : index+1], err
						}))
					}
					reg.Register(coreRegistryStep(func(ctx context.Context, _ graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
						laterReads++
						rows, err := q.ListResources(ctx, graph.Filter{Kind: "Pod", ClusterID: "west"})
						if err != nil || len(rows) != 1 || rows[0].ID() != targets[cluster].ID() {
							t.Error("recovered registry escaped member scope", rows, err)
						}
						return nil, err
					}))
					obj := resyncObject("ConfigMap", "registry", "uid", "version")
					r := UnstructuredToResource(obj, "ConfigMap")
					r.ClusterID = cluster
					m.handleUpsert(ctx, configMapGVR, obj, graph.EventTypeAdd)
					if ctx.Err() == nil || laterReads != 0 {
						t.Fatal("cancelled registry began later target reads", laterReads)
					}
					stored, err := store.GetResource(context.Background(), r.ID())
					if err != nil || !graph.SameResourceVersion(stored, r) {
						t.Fatal("cancellation lost committed resource", stored, err)
					}
					rows, err := store.ListResourcesInCluster(context.Background(), cluster, graph.Filter{Kind: "Secret"})
					if err != nil || len(rows) != 0 {
						t.Fatal("cancelled batch created Secret references", rows, err)
					}
					edges, err := store.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
					if err != nil || len(edges) != 0 || len(sink.recorded()) != 1 {
						t.Fatal("cancelled batch wrote edges or changed committed history", edges, sink.recorded(), err)
					}
					before := coverageView(t, tracker, cluster, configMapGVR)
					if !before.ExtractionGapSince.Equal(prior) || !before.PersistenceGapSince.IsZero() || !before.ProcessingGapSince.IsZero() {
						t.Fatal("teardown misclassified cancellation as new loss", before)
					}
					active = false
					for range 2 {
						m.handleUpsert(context.Background(), configMapGVR, obj, graph.EventTypeUpdate)
					}
					edges, err = store.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
					if err != nil || len(edges) != 2 || laterReads != 2 || len(sink.recorded()) != 1 {
						t.Fatal("replay failed to recover edges or duplicated history", edges, laterReads, sink.recorded(), err)
					}
					after := coverageView(t, tracker, cluster, configMapGVR)
					if !after.ExtractionGapSince.Equal(prior) || !after.PersistenceGapSince.IsZero() || !after.ProcessingGapSince.IsZero() {
						t.Fatal("replay erased prior gap or introduced loss", after)
					}
				})
			}
		}
	}
}
