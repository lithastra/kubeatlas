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

type partialConstraintStore struct {
	*memory.Store
	read func(context.Context, string, graph.Filter) ([]graph.Resource, error)
	edge func(context.Context, graph.Edge) error
}

func (s *partialConstraintStore) ListResourcesInCluster(ctx context.Context, cluster string, f graph.Filter) ([]graph.Resource, error) {
	return s.read(ctx, cluster, f)
}

func (s *partialConstraintStore) UpsertEdge(ctx context.Context, e graph.Edge) error {
	return s.edge(ctx, e)
}

func TestConstraintPartialExtractionKeepsScopedEvidence(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, scenario := range []string{"builtin-read", "valid-partial", "foreign-from", "foreign-to", "empty-endpoint", "write-failure", "cancel-extraction", "cancel-edge"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/%s", cluster, history, scenario), func(t *testing.T) {
					testConstraintPartialExtraction(t, cluster, history, scenario)
				})
			}
		}
	}
}

func testConstraintPartialExtraction(t *testing.T, cluster string, history bool, scenario string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := memory.New()
	store := &partialConstraintStore{Store: base}
	tracker := operations.NewCoverageTracker(time.Minute)
	source, err := tracker.BeginSource(cluster, operations.CoverageSourceGatekeeper, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, err := source.BeginType(coverageConstraintGVR, time.Now())
	if err != nil || !token.BindOrigin(graph.ResourceOrigin{OwnerName: "k8srequiredlabels", OwnerUID: "template-uid", Group: constraintGroup, Version: constraintVersion, Resource: coverageConstraintGVR.Resource, Kind: "K8sRequiredLabels"}) {
		t.Fatal("could not bind the source receipt", err)
	}
	priorGap := time.Now().Add(-time.Minute)
	token.RecordGap(coverageConstraintGVR, operations.CoverageProcessingFailed, priorGap)
	_, object := coverageObjects()
	object.Object["spec"] = map[string]any{"match": map[string]any{
		"kinds": []any{map[string]any{"kinds": []any{"Pod", "Deployment"}}}, "namespaces": []any{"default"},
	}}
	object.Object["status"] = map[string]any{"violations": []any{map[string]any{"kind": "Pod", "namespace": "default", "name": "same", "message": "missing label"}}}
	r := discovery.UnstructuredToResource(object, "K8sRequiredLabels")
	r.ClusterID = cluster
	for _, owner := range []string{"east", "west", ""} {
		for _, kind := range []string{"Pod", "Deployment"} {
			if err := base.UpsertResource(ctx, graph.Resource{ClusterID: owner, Namespace: "default", Kind: kind, Name: "same"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	active, reads, writes := true, 0, 0
	store.read = func(ctx context.Context, owner string, f graph.Filter) ([]graph.Resource, error) {
		reads++
		if owner != cluster || f.ClusterID != cluster {
			t.Fatal("constraint target lookup escaped its scope", owner, f)
		}
		rows, err := base.ListResourcesInCluster(ctx, owner, f)
		if active && scenario == "builtin-read" && f.Kind == "Deployment" {
			return rows, errors.New("injected target lookup failure")
		}
		return rows, err
	}
	store.edge = func(_ context.Context, edge graph.Edge) error {
		writes++
		if active && writes == 1 {
			if scenario == "write-failure" {
				return errors.New("injected edge write failure")
			}
			if scenario == "cancel-edge" {
				cancel() // A write already in flight can still commit.
			}
		}
		return base.UpsertEdge(context.Background(), edge)
	}
	validEdges := []graph.Edge{
		{From: "/K8sRequiredLabels/test-labels", To: "default/Pod/same", Type: graph.EdgeTypeEnforces},
		{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Namespace: "default", Kind: "Deployment", Name: "same"}).ID(), Type: graph.EdgeTypeEnforces},
	}
	ext := coverageExtractFunc(func(_ context.Context, _ graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
		edges := append([]graph.Edge(nil), validEdges...)
		if !active {
			return edges, nil
		}
		switch scenario {
		case "foreign-from":
			edges[1].From = "west:/K8sRequiredLabels/foreign"
		case "foreign-to":
			edges[1].To = "west:default/Pod/foreign"
		case "empty-endpoint":
			edges[1].To = ""
		case "cancel-extraction":
			cancel()
		}
		return edges, errors.New("injected partial extraction failure")
	})
	d := New(nil, store, ext, nil, WithClusterID(cluster), WithMemberRepair(discovery.NewMemberRepair(nil, store, cluster)))
	if scenario == "builtin-read" {
		d.extractor = extractor.Default()
	}
	sink := &dynamicHistorySink{}
	if history {
		d.resourceHistory = discovery.NewResourceHistory(store, cluster, sink)
		if cluster == "" {
			d.resourceHistory = discovery.NewStandaloneResourceHistory(store, sink)
		}
	}
	d.handleConstraintEvent(ctx, r.Kind, object, token, graph.EventTypeAdd)
	row := coverageRow(memberGKSource(t, tracker, cluster), coverageConstraintGVR)
	wantGap := scenario != "cancel-extraction"
	if row.ExtractionGapSince.IsZero() == wantGap || !row.ProcessingGapSince.Equal(priorGap) || row.PersistenceGapSince.IsZero() != (scenario != "write-failure") {
		t.Fatal("partial failure lost or misclassified gap evidence", row)
	}
	wantEdges, wantWrites := 2, 2
	switch scenario {
	case "builtin-read":
		wantEdges, wantWrites = 1, 1
		if reads != 2 {
			t.Fatal("failed target query was retried or skipped", reads)
		}
	case "foreign-from", "foreign-to", "empty-endpoint", "cancel-extraction":
		wantEdges, wantWrites = 0, 0
	case "write-failure":
		wantEdges = 1
	case "cancel-edge":
		wantEdges, wantWrites = 1, 1
	}
	edges, err := base.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != wantEdges || writes != wantWrites {
		t.Fatal("partial callback persisted the wrong batch", edges, writes, err)
	}
	for _, edge := range edges {
		if edge.From != r.ID() {
			t.Fatal("partial callback failed to qualify a relative endpoint", edge)
		}
	}
	if scenario == "builtin-read" && edges[0].Attributes["violation_message"] != "missing label" {
		t.Fatal("target read failure discarded confirmed violation observation", edges)
	}
	stored, err := base.GetResource(context.Background(), r.ID())
	if err != nil || !stored.Origin.Matches(stored) {
		t.Fatal("partial extraction lost the committed source receipt", stored, err)
	}
	// A later normal delivery can repair edges, but cannot rewrite the prior gaps
	// or create another resource-history event for the unchanged source object.
	active = false
	d.handleConstraintEvent(context.Background(), r.Kind, object, token, graph.EventTypeUpdate)
	edges, err = base.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 2 {
		t.Fatal("healthy delivery failed to repair scoped edges", edges, err)
	}
	after := coverageRow(memberGKSource(t, tracker, cluster), coverageConstraintGVR)
	if !after.ExtractionGapSince.Equal(row.ExtractionGapSince) || !after.PersistenceGapSince.Equal(row.PersistenceGapSince) || !after.ProcessingGapSince.Equal(priorGap) {
		t.Fatal("healthy delivery erased prior failure evidence", after)
	}
	retained, err := base.GetResource(context.Background(), r.ID())
	if err != nil || retained.Origin != stored.Origin {
		t.Fatal("edge recovery changed the source receipt", retained, err)
	}
	wantHistory := 0
	if history {
		wantHistory = 1
	}
	if len(sink.events) != wantHistory {
		t.Fatal("edge recovery changed committed resource history", sink.events)
	}
}
