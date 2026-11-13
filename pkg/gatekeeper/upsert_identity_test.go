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
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type identityUpsertStore struct {
	*memory.Store
	hook func() error
}

func (s *identityUpsertStore) UpsertResource(_ context.Context, r graph.Resource) error {
	if s.hook != nil {
		if err := s.hook(); err != nil {
			return err
		}
	}
	// An already-started dependency can commit after cancellation.
	return s.Store.UpsertResource(context.Background(), r)
}

func TestObservedUpsertIdentityQualification(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, missing := range []string{"valid", "uid", "version", "api-version"} {
				for _, mode := range []string{"active", "write-failure", "canceled-write", "canceled-commit"} {
					t.Run(fmt.Sprintf("cluster=%s/history=%t/%s/%s", cluster, history, missing, mode), func(t *testing.T) {
						testObservedUpsertIdentity(t, cluster, history, missing, mode)
					})
				}
			}
		}
	}
}

func testObservedUpsertIdentity(t *testing.T, cluster string, history bool, missing, mode string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &identityUpsertStore{Store: memory.New()}
	tracker := operations.NewCoverageTracker(time.Minute)
	extractions := 0
	extract := func(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
		extractions++
		return []graph.Edge{{From: r.ID(), To: r.ID(), Type: graph.EdgeTypeOwns}}, nil
	}
	gvr := coverageConstraintGVR
	_, full := coverageObjects()
	source, err := tracker.BeginSource(cluster, operations.CoverageSourceGatekeeper, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, err := source.BeginType(gvr, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !token.BindOrigin(graph.ResourceOrigin{OwnerName: "k8srequiredlabels", OwnerUID: "template-uid", Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Kind: full.GetKind()}) {
		t.Fatal("origin binding failed")
	}
	sink := &dynamicHistorySink{}
	collector := New(nil, store, coverageExtractFunc(extract), nil, WithClusterID(cluster), WithMemberRepair(discovery.NewMemberRepair(nil, store, cluster)))
	if history {
		collector.resourceHistory = discovery.NewResourceHistory(store, cluster, sink)
		if cluster == "" {
			collector.resourceHistory = discovery.NewStandaloneResourceHistory(store, sink)
		}
	}
	observe := func(ctx context.Context, obj *unstructured.Unstructured, event graph.EventType) {
		collector.handleConstraintEvent(ctx, full.GetKind(), obj, token, event)
	}
	recorded := func() []graph.ResourceEvent { return sink.events }
	readCoverage := func() operations.ResourceCoverage { return coverageRow(memberGKSource(t, tracker, cluster), gvr) }
	recordsHistory := history
	token.InitialDeliveryComplete(gvr)
	token.ListResult(gvr, true, false, time.Now())
	token.WatchStarted(gvr)
	if row := readCoverage(); row.State != operations.CoverageObserved {
		t.Fatal("fixture did not start observed", row)
	}
	prior := time.Time{}
	if mode != "active" {
		prior = time.Now().Add(-time.Minute)
		token.RecordGap(gvr, operations.CoverageExtractionFailed, prior)
	}
	object := full.DeepCopy()
	switch missing {
	case "uid":
		object.SetUID("")
	case "version":
		object.SetResourceVersion("")
	case "api-version":
		object.SetAPIVersion("")
	}
	resource := discovery.UnstructuredToResource(object, full.GetKind())
	resource.ClusterID = cluster
	foreign := discovery.UnstructuredToResource(full, full.GetKind())
	foreign.ClusterID = "west"
	if err := store.Store.UpsertResource(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	store.hook = func() error {
		switch mode {
		case "write-failure":
			return errors.New("injected identity write failure")
		case "canceled-write":
			cancel()
			return ctx.Err()
		case "canceled-commit":
			cancel()
		}
		return nil
	}
	observe(ctx, object, graph.EventTypeAdd)
	committed := mode == "active" || mode == "canceled-commit"
	got, err := store.GetResource(context.Background(), resource.ID())
	if committed {
		if err != nil || got.UID != object.GetUID() || got.ResourceVersion != object.GetResourceVersion() || got.GroupVersion != object.GetAPIVersion() {
			t.Fatal("changed committed unqualified observation", got, err)
		}
		if missing != "valid" && got.Origin != (graph.ResourceOrigin{}) {
			t.Fatal("unqualified identity acquired private provenance", got.Origin)
		}
	} else if !errors.As(err, new(graph.ErrNotFound)) {
		t.Fatal("failed write changed graph", got, err)
	}
	row := readCoverage()
	wantProcessing := missing != "valid" && mode == "active"
	if !row.ProcessingGapSince.IsZero() != wantProcessing || !row.PersistenceGapSince.IsZero() != (mode == "write-failure") || !row.ExtractionGapSince.Equal(prior) {
		t.Fatal("history mode or cancellation changed identity qualification", row)
	}
	if mode == "active" {
		wantState := operations.CoverageObserved
		if missing != "valid" {
			wantState = operations.CoveragePartial
		}
		if row.State != wantState {
			t.Fatal("incomplete identity still qualified as observed", row)
		}
	}
	wantExtractions := 0
	if mode == "active" {
		wantExtractions = 1
	}
	if extractions != wantExtractions {
		t.Fatal("write failure/cancellation began extraction", extractions)
	}
	wantEvents := 0
	if missing == "valid" && committed && recordsHistory {
		wantEvents = 1
	}
	if len(recorded()) != wantEvents {
		t.Fatal("unqualified/uncommitted identity produced history", recorded())
	}

	store.hook = nil
	observe(context.Background(), full, graph.EventTypeUpdate)
	observe(context.Background(), full.DeepCopy(), graph.EventTypeUpdate)
	got, err = store.GetResource(context.Background(), resource.ID())
	if err != nil || !graph.ValidResourceVersion(got) || got.UID != full.GetUID() || got.ResourceVersion != full.GetResourceVersion() || got.GroupVersion != full.GetAPIVersion() {
		t.Fatal("fresh complete identity did not repair stored observation", got, err)
	}
	wantEvents = 0
	if recordsHistory {
		wantEvents = 1
	}
	if len(recorded()) != wantEvents {
		t.Fatal("identity repair lost or duplicated qualified history", recorded())
	}
	if extractions != wantExtractions+2 {
		t.Fatal("later identity repair skipped edge recovery", extractions)
	}
	after := readCoverage()
	if !after.ProcessingGapSince.Equal(row.ProcessingGapSince) || !after.PersistenceGapSince.Equal(row.PersistenceGapSince) || !after.ExtractionGapSince.Equal(prior) {
		t.Fatal("identity repair erased historical gaps", after)
	}
	retained, err := store.GetResource(context.Background(), foreign.ID())
	if err != nil || !graph.SameResourceVersion(retained, foreign) {
		t.Fatal("identity qualification changed another member", retained, err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil || len(snapshot.Resources) != 2 || len(snapshot.Edges) != 1 {
		t.Fatal("identity repair removed data or added unexpected graph rows", snapshot, err)
	}
}
