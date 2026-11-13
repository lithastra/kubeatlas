// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
package discovery

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestMemberEdgeScopeQualificationAndRejection(t *testing.T) {
	for _, tc := range []struct {
		from, to, wantFrom, wantTo string
		fail                       bool
	}{
		{"default/Foo/demo", "/Bar/target", "east:default/Foo/demo", "east:/Bar/target", false},
		{"east:default/Foo/demo", "_external/IAMRole/arn:aws:iam::123:role/demo", "east:default/Foo/demo", "east:_external/IAMRole/arn:aws:iam::123:role/demo", false},
		{"west:default/Foo/demo", "/Bar/target", "", "", true},
		{"default/Foo/demo", "west:/Bar/target", "", "", true},
		{"", "/Bar/target", "", "", true},
	} {
		input := []graph.Edge{{From: tc.from, To: tc.to, Type: graph.EdgeTypeUsesConfigMap}}
		original := append([]graph.Edge(nil), input...)
		out, err := ScopeEdgesToCluster(input, "east")
		if (err != nil) != tc.fail {
			t.Fatalf("wrong rejection for %+v: %v", tc, err)
		}
		if !tc.fail && (out[0].From != tc.wantFrom || out[0].To != tc.wantTo) {
			t.Fatalf("wrong member projection %+v", out)
		}
		if !reflect.DeepEqual(input, original) {
			t.Fatal("projection mutated caller's cached edges")
		}
		single, err := ScopeEdgesToCluster(input, "")
		if err != nil || !reflect.DeepEqual(single, input) {
			t.Fatal("standalone edge contract changed")
		}
	}
	edges := []graph.Edge{{From: "ns/Foo/demo", To: "/Bar/good"}, {From: "ns/Foo/demo", To: "west:/Bar/foreign"}}
	if out, err := ScopeEdgesToCluster(edges, "east"); err == nil || out != nil {
		t.Fatal("foreign endpoint returned a persistable partial set")
	}
}

func TestCollectorEdgeScopeIsExplicitAndBatchAtomic(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, endpoint := range []string{"ns/Foo/item", "east:ns/Foo/item", "west:ns/Foo/item", "", ":ns/Foo/item", "_external/IAMRole/arn:aws:iam::123:role/demo"} {
			t.Run(cluster+"/"+endpoint, func(t *testing.T) {
				input := []graph.Edge{{From: "ns/Pod/own", To: "ns/Secret/valid"}, {From: "ns/Pod/own", To: endpoint}}
				original := append([]graph.Edge(nil), input...)
				out, err := ScopeCollectorEdges(input, cluster)
				invalid := endpoint == "" || endpoint == ":ns/Foo/item" || endpoint == "west:ns/Foo/item" || (cluster == "" && endpoint == "east:ns/Foo/item")
				if invalid {
					if err == nil || out != nil {
						t.Fatal("invalid endpoint returned a persistable partial batch", out, err)
					}
				} else {
					want := endpoint
					if cluster != "" && endpoint != "east:ns/Foo/item" {
						want = cluster + ":" + endpoint
					}
					if err != nil || len(out) != 2 || out[1].To != want {
						t.Fatal("valid endpoint lost scope or external colons", out, err)
					}
					out[0].From = "changed"
				}
				if !reflect.DeepEqual(input, original) {
					t.Fatal("collector projection mutated caller edges")
				}
			})
		}
	}
}

type collectorQueryStore struct {
	*memory.Store
	unscoped, scoped int
	cluster          string
	filter           graph.Filter
	err              error
	rows             []graph.Resource
	cancel           context.CancelFunc
}

func (s *collectorQueryStore) ListResources(context.Context, graph.Filter) ([]graph.Resource, error) {
	s.unscoped++
	return nil, errors.New("unscoped query must not be used")
}

func (s *collectorQueryStore) ListResourcesInCluster(ctx context.Context, cluster string, filter graph.Filter) ([]graph.Resource, error) {
	s.scoped++
	s.cluster, s.filter = cluster, filter
	if s.cancel != nil {
		s.cancel()
	}
	if s.rows != nil || s.err != nil {
		return s.rows, s.err
	}
	return s.Store.ListResourcesInCluster(ctx, cluster, filter)
}

func TestScopedResourceListerPreservesFiltersAndRejectsFaults(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, scenario := range []string{"scoped", "read-error", "foreign-row", "pre-canceled", "mid-read-canceled"} {
			t.Run(cluster+"/"+scenario, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				s := &collectorQueryStore{Store: memory.New()}
				scopeTargets(t, s, "Pod")
				for _, r := range []graph.Resource{
					{ClusterID: cluster, Kind: "Pod", Namespace: "test", Name: "wrong-label", Labels: map[string]string{"app": "other"}},
					{ClusterID: cluster, Kind: "Pod", Namespace: "other", Name: "wrong-namespace", Labels: map[string]string{"app": "selected"}},
					{ClusterID: cluster, Kind: "Deployment", Namespace: "test", Name: "wrong-kind", Labels: map[string]string{"app": "selected"}},
				} {
					if err := s.UpsertResource(ctx, r); err != nil {
						t.Fatal(err)
					}
				}
				filter := graph.Filter{ClusterID: "west", Kind: "Pod", Namespace: "test", Labels: map[string]string{"app": "selected"}}
				switch scenario {
				case "read-error":
					s.rows, s.err = []graph.Resource{{ClusterID: cluster}}, errors.New("injected read failure")
				case "foreign-row":
					s.rows = []graph.Resource{{ClusterID: cluster}, {ClusterID: "west"}}
				case "pre-canceled":
					cancel()
				case "mid-read-canceled":
					s.cancel = cancel
				}
				q := NewScopedResourceLister(s, cluster)
				if _, broad := q.(graph.GraphStore); broad {
					t.Fatal("scoped read adapter exposed mutation capability")
				}
				rows, err := q.ListResources(ctx, filter)
				if scenario == "scoped" {
					if err != nil || len(rows) != 1 || rows[0].ClusterID != cluster || rows[0].Name != "same" {
						t.Fatal("scope or non-scope filters were not preserved", rows, err)
					}
				} else if err == nil || rows != nil {
					t.Fatal("fault exposed partial/foreign results", rows, err)
				}
				wantReads := 1
				if scenario == "pre-canceled" {
					wantReads = 0
				}
				if s.unscoped != 0 || s.scoped != wantReads || filter.ClusterID != "west" {
					t.Fatal("query fell back/retried or mutated caller filter", s.unscoped, s.scoped, filter)
				}
				if wantReads == 1 && (s.cluster != cluster || s.filter.ClusterID != cluster || s.filter.Kind != filter.Kind || s.filter.Namespace != filter.Namespace || !reflect.DeepEqual(s.filter.Labels, filter.Labels)) {
					t.Fatal("scoped query changed requested filters", s.cluster, s.filter)
				}
			})
		}
	}
}
