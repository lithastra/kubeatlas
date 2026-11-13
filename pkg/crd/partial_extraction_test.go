// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package crd

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	regorules "github.com/lithastra/kubeatlas/pkg/extractor/rego"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

type partialCRDStore struct {
	*memory.Store
	edge func(context.Context, graph.Edge) error
}

func (s *partialCRDStore) UpsertEdge(ctx context.Context, edge graph.Edge) error {
	return s.edge(ctx, edge)
}

func TestCRDPartialExtractionKeepsScopedEvidence(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, scenario := range []string{"real-engine", "valid-partial", "foreign-from", "foreign-to", "empty-endpoint", "write-failure", "cancel-success", "cancel-error", "cancel-edge"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/%s", cluster, history, scenario), func(t *testing.T) {
					testCRDPartialExtraction(t, cluster, history, scenario)
				})
			}
		}
	}
}

func testCRDPartialExtraction(t *testing.T, cluster string, history bool, scenario string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := memory.New()
	store := &partialCRDStore{Store: base}
	tracker := operations.NewCoverageTracker(time.Minute)
	source, err := tracker.BeginSource(cluster, operations.CoverageSourceCRD, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, err := source.BeginType(coverageFooGVR, time.Now())
	if err != nil || !token.BindOrigin(graph.ResourceOrigin{OwnerName: "foos.example.com", OwnerUID: "definition-uid", Group: "example.com", Version: "v1", Resource: "foos", Kind: "Foo", Namespaced: true}) {
		t.Fatal("could not bind the source receipt", err)
	}
	priorGap := time.Now().Add(-time.Minute)
	token.RecordGap(coverageFooGVR, operations.CoverageProcessingFailed, priorGap)
	_, obj := coverageObjects(t)
	r := discovery.UnstructuredToResource(obj, "Foo")
	r.ClusterID = cluster
	foreign := r
	foreign.ClusterID = "west"
	if err := base.UpsertResource(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	active, writes := true, 0
	store.edge = func(_ context.Context, edge graph.Edge) error {
		writes++
		if active && writes == 1 {
			switch scenario {
			case "write-failure":
				return errors.New("injected CRD edge write failure")
			case "cancel-edge":
				cancel() // An already-started write may commit after cancellation.
			}
		}
		return base.UpsertEdge(context.Background(), edge)
	}
	rules := coverageRegoFunc(func(context.Context, graph.Resource) ([]graph.Edge, error) {
		edges := []graph.Edge{
			{From: "default/Foo/demo", To: "default/Bar/first", Type: graph.EdgeTypeOwns},
			{From: r.ID(), To: "default/Bar/second", Type: graph.EdgeTypeOwns},
		}
		if !active {
			return edges, nil
		}
		switch scenario {
		case "foreign-from":
			edges[1].From = "west:default/Foo/foreign"
		case "foreign-to":
			edges[1].To = "west:default/Bar/foreign"
		case "empty-endpoint":
			edges[1].To = ""
		case "cancel-success":
			cancel()
			return edges, nil
		case "cancel-error":
			cancel()
		}
		return edges, errors.New("injected partial rule failure")
	})
	d := New(nil, store, WithClusterID(cluster), WithMemberRepair(discovery.NewMemberRepair(nil, store, cluster)), WithRegoEvaluator(rules))
	var engine *regorules.Engine
	if scenario == "real-engine" {
		engine = newPartialCRDEngine(t)
		d.rego = engine
	}
	sink := &dynamicHistorySink{}
	if history {
		d.resourceHistory = discovery.NewResourceHistory(store, cluster, sink)
		if cluster == "" {
			d.resourceHistory = discovery.NewStandaloneResourceHistory(store, sink)
		}
	}
	d.handleUpsertEvent(ctx, coverageFooGVR, "Foo", obj, token, graph.EventTypeAdd)
	row := coverageRow(memberCRDSource(t, tracker, cluster), coverageFooGVR)
	wantGap := scenario != "cancel-success" && scenario != "cancel-error"
	if row.ExtractionGapSince.IsZero() == wantGap || !row.ProcessingGapSince.Equal(priorGap) || row.PersistenceGapSince.IsZero() != (scenario != "write-failure") {
		t.Fatal("partial failure lost or misclassified gap evidence", row)
	}
	wantEdges, wantWrites := 2, 2
	switch scenario {
	case "real-engine", "cancel-edge":
		wantEdges, wantWrites = 1, 1
	case "foreign-from", "foreign-to", "empty-endpoint", "cancel-success", "cancel-error":
		wantEdges, wantWrites = 0, 0
	case "write-failure":
		wantEdges = 1
	}
	edges, err := base.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != wantEdges || writes != wantWrites {
		t.Fatal("partial callback persisted the wrong batch", edges, writes, err)
	}
	for _, edge := range edges {
		if edge.From != r.ID() || (cluster != "" && edge.To != cluster+":default/Bar/first" && edge.To != cluster+":default/Bar/second") {
			t.Fatal("partial callback failed to bind member scope", edge)
		}
	}
	stored, err := base.GetResource(context.Background(), r.ID())
	if err != nil || !stored.Origin.Matches(stored) {
		t.Fatal("partial extraction lost the committed source receipt", stored, err)
	}
	// A later normal delivery repairs edges while retaining the original gap,
	// writer receipt and single committed metadata-only resource history event.
	active = false
	if engine != nil {
		if err := engine.LoadModule(context.Background(), "partial/second", crdPartialModule("second"), "data.partial.derive"); err != nil {
			t.Fatal(err)
		}
	}
	d.handleUpsertEvent(context.Background(), coverageFooGVR, "Foo", obj, token, graph.EventTypeUpdate)
	edges, err = base.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 2 {
		t.Fatal("healthy delivery failed to repair scoped edges", edges, err)
	}
	after := coverageRow(memberCRDSource(t, tracker, cluster), coverageFooGVR)
	if !after.ExtractionGapSince.Equal(row.ExtractionGapSince) || !after.PersistenceGapSince.Equal(row.PersistenceGapSince) || !after.ProcessingGapSince.Equal(priorGap) {
		t.Fatal("healthy delivery erased prior failure evidence", after)
	}
	retained, err := base.GetResource(context.Background(), r.ID())
	if err != nil || retained.Origin != stored.Origin {
		t.Fatal("edge recovery changed the source receipt", retained, err)
	}
	if got, err := base.GetResource(context.Background(), foreign.ID()); err != nil || got.UID != foreign.UID || got.ResourceVersion != foreign.ResourceVersion || got.ClusterID != foreign.ClusterID || got.Origin != foreign.Origin {
		t.Fatal("partial extraction changed another member's source", got, err)
	}
	if edges, err := base.ListEdges(context.Background(), foreign.ID(), graph.DirectionOutgoing); err != nil || len(edges) != 0 {
		t.Fatal("partial extraction wrote another member's edges", edges, err)
	}
	wantHistory := 0
	if history {
		wantHistory = 1
	}
	if len(sink.events) != wantHistory {
		t.Fatal("edge recovery changed committed resource history", sink.events)
	}
	for _, event := range sink.events {
		if event.Data != nil || event.ClusterID != cluster || event.EventType != graph.EventTypeAdd {
			t.Fatal("partial extraction changed metadata-only history", event)
		}
	}
}

func crdPartialModule(target string) string {
	return fmt.Sprintf(`package partial
import rego.v1
derive := [{"type":"OWNS", "from":{"kind":input.kind,"namespace":input.metadata.namespace,"name":input.metadata.name}, "to":{"kind":"Bar","namespace":input.metadata.namespace,"name":%q}}]
`, target)
}

func newPartialCRDEngine(t *testing.T) *regorules.Engine {
	t.Helper()
	router := regorules.NewRouter()
	cache, err := regorules.NewCache(100, nil)
	if err != nil {
		t.Fatal(err)
	}
	engine := regorules.New(regorules.WithRouter(router), regorules.WithCache(cache))
	for _, name := range []string{"first", "second"} {
		router.Register("partial", &regorules.ModuleSpec{Name: name, Match: regorules.GVKMatch{Group: "example.com", Kind: "Foo"}})
		source := crdPartialModule(name)
		if name == "second" {
			source = "package partial\nimport rego.v1\nderive := [{\"type\":\"OWNS\"}]\n"
		}
		if err := engine.LoadModule(context.Background(), "partial/"+name, source, "data.partial.derive"); err != nil {
			t.Fatal(err)
		}
	}
	return engine
}
