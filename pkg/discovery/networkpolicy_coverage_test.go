// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type networkPolicyReadStore struct {
	*memory.Store
	read func(context.Context, string, graph.Filter) ([]graph.Resource, error)
}

func (s *networkPolicyReadStore) ListResourcesInCluster(ctx context.Context, cluster string, f graph.Filter) ([]graph.Resource, error) {
	return s.read(ctx, cluster, f)
}

var networkPolicyCoverageGVR = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}

func TestCoreNetworkPolicyPartialReadCoverageAndRecovery(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, direction := range []string{"ingress", "egress"} {
				for _, scenario := range []string{"read-failure", "cancel", "empty-namespaces"} {
					t.Run(fmt.Sprintf("cluster=%s/history=%t/%s/%s", cluster, history, direction, scenario), func(t *testing.T) {
						testCoreNetworkPolicyCoverage(t, cluster, history, direction, scenario)
					})
				}
			}
		}
	}
}

func testCoreNetworkPolicyCoverage(t *testing.T, cluster string, history bool, direction, scenario string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := memory.New()
	store := &networkPolicyReadStore{Store: base}
	tracker := operations.NewCoverageTracker(time.Minute)
	var notifications int
	m := NewInformerManager(nil, store, WithClusterID(cluster), WithExtractor(extractor.Default()), WithMemberRepair(NewMemberRepair(nil, store, cluster)),
		WithBroadcaster(func(_, _, _, _ string) { notifications++ }))
	var err error
	m.coverageSession, err = tracker.Begin(cluster, []schema.GroupVersionResource{networkPolicyCoverageGVR}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	priorGap := time.Now().Add(-time.Minute)
	m.coverageSession.RecordGap(networkPolicyCoverageGVR, operations.CoverageProcessingFailed, priorGap)
	sink := &resyncSink{}
	if history {
		cancellationHistory(m, store, cluster, sink)
	}
	object := resyncObject("NetworkPolicy", "policy", "policy-uid", "version")
	object.SetAPIVersion("networking.k8s.io/v1")
	field, peerField, policyType, edgeType := "ingress", "from", "Ingress", graph.EdgeTypeAllowsFrom
	if direction == "egress" {
		field, peerField, policyType, edgeType = "egress", "to", "Egress", graph.EdgeTypeAllowsTo
	}
	object.Object["spec"] = map[string]any{
		"podSelector": map[string]any{}, "policyTypes": []any{policyType},
		field: []any{map[string]any{peerField: []any{map[string]any{"podSelector": map[string]any{}, "namespaceSelector": map[string]any{}}}}},
	}
	r := UnstructuredToResource(object, "NetworkPolicy")
	r.ClusterID = cluster
	for _, owner := range []string{"east", "west", ""} {
		for _, ns := range []string{"test", "alpha", "beta"} {
			if err := base.UpsertResource(ctx, graph.Resource{ClusterID: owner, Namespace: ns, Kind: "Pod", Name: "same"}); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"alpha", "beta"} {
			if err := base.UpsertResource(ctx, graph.Resource{ClusterID: owner, Kind: "Namespace", Name: name}); err != nil {
				t.Fatal(err)
			}
		}
	}
	active := true
	var queries []string
	store.read = func(_ context.Context, owner string, f graph.Filter) ([]graph.Resource, error) {
		if owner != cluster || f.ClusterID != cluster {
			t.Fatal("network policy query escaped its owning scope", owner, f)
		}
		queries = append(queries, f.Kind+"/"+f.Namespace)
		rows, err := base.ListResourcesInCluster(context.Background(), owner, f)
		if f.Kind == "Namespace" {
			// Make the injected later failure independent of memory-map iteration.
			sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
			if active && scenario == "empty-namespaces" {
				return nil, nil
			}
		}
		if active && f.Namespace == "beta" {
			switch scenario {
			case "cancel":
				cancel() // Deliberately return successful rows after cancellation.
			case "read-failure":
				return rows, errors.New("injected namespace read failure")
			}
		}
		return rows, err
	}
	m.handleUpsert(ctx, networkPolicyCoverageGVR, object, graph.EventTypeAdd)
	row := coverageView(t, tracker, cluster, networkPolicyCoverageGVR)
	gotGap, wantGap := !row.ExtractionGapSince.IsZero(), scenario == "read-failure"
	if gotGap != wantGap || !row.PersistenceGapSince.IsZero() || !row.ProcessingGapSince.Equal(priorGap) {
		t.Fatal("network policy read misclassified coverage gaps", row)
	}
	wantQueries := []string{"/test", "Namespace/", "/alpha", "/beta"}
	wantEdges := 2 // The selection edge plus the successfully read alpha peer.
	switch scenario {
	case "cancel":
		wantEdges = 0
	case "empty-namespaces":
		wantEdges, wantQueries = 1, wantQueries[:2]
	}
	edges, err := base.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != wantEdges || !reflect.DeepEqual(queries, wantQueries) {
		t.Fatal("partial callback lost edges, accepted failed rows or retried", edges, queries, err)
	}
	for _, edge := range edges {
		selection := graph.Resource{ClusterID: cluster, Namespace: "test", Kind: "Pod", Name: "same"}
		peer := graph.Resource{ClusterID: cluster, Namespace: "alpha", Kind: "Pod", Name: "same"}
		validSelection := edge.Type == graph.EdgeTypeSelectsNP && edge.To == selection.ID()
		validPeer := edge.Type == edgeType && edge.To == peer.ID()
		if edge.From != r.ID() || (!validSelection && !validPeer) {
			t.Fatal("partial result contains a foreign or failed target", edge)
		}
	}
	if _, err := base.GetResource(context.Background(), r.ID()); err != nil || notifications != 1 {
		t.Fatal("read failure/cancellation lost committed resource or notification", notifications, err)
	}
	active = false
	m.handleUpsert(context.Background(), networkPolicyCoverageGVR, object, graph.EventTypeUpdate)
	edges, err = base.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 3 || notifications != 2 {
		t.Fatal("normal delivery did not recover the missing relationships", edges, notifications, err)
	}
	after := coverageView(t, tracker, cluster, networkPolicyCoverageGVR)
	if !after.ExtractionGapSince.Equal(row.ExtractionGapSince) || !after.ProcessingGapSince.Equal(priorGap) || !after.PersistenceGapSince.IsZero() {
		t.Fatal("normal delivery changed historical loss evidence", after)
	}
	wantEvents := 0
	if history {
		wantEvents = 1
	}
	if len(sink.recorded()) != wantEvents {
		t.Fatal("edge recovery fabricated resource history", sink.recorded())
	}
	snapshot, err := base.Snapshot(context.Background())
	if err != nil || len(snapshot.Resources) != 16 {
		t.Fatal("partial reads removed retained resources or created placeholders", len(snapshot.Resources), err)
	}
}
