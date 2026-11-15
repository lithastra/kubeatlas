// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// RunResourceVersionDelete is shared by Tier 1 and Tier 2. It exercises the
// optional repair capability without changing the GraphStore v2 contract.
func RunResourceVersionDelete(t *testing.T, factory Factory) {
	t.Helper()
	for _, change := range []string{"none", "uid", "version", "api", "cluster", "missing", "invalid", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			s := factory(t)
			d, ok := s.(graph.ResourceVersionDeleter)
			if !ok {
				t.Fatal("store lacks atomic resource-version deletion")
			}
			old := graph.Resource{ClusterID: "east", Namespace: "demo", Kind: "Pod", Name: "same", GroupVersion: "v1", UID: "old-uid", ResourceVersion: "7"}
			current := old
			switch change {
			case "uid":
				current.UID = "new-uid"
			case "version":
				current.ResourceVersion = "8"
			case "api":
				current.GroupVersion = "other/v1"
			case "cluster":
				current.ClusterID = "west"
			}
			target := graph.Resource{ClusterID: current.ClusterID, Namespace: "demo", Kind: "ConfigMap", Name: "target"}
			if err := s.UpsertResource(ctx, current); err != nil {
				t.Fatal(err)
			}
			if err := s.UpsertResource(ctx, target); err != nil {
				t.Fatal(err)
			}
			edge := graph.Edge{From: current.ID(), To: target.ID(), Type: graph.EdgeTypeUsesConfigMap}
			if err := s.UpsertEdge(ctx, edge); err != nil {
				t.Fatal(err)
			}
			if change == "missing" {
				old.Name = "unknown"
			}
			if change == "invalid" {
				old.UID = ""
			}
			if change == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			deleted, err := d.DeleteResourceVersion(ctx, old)
			wantError := change == "invalid" || change == "cancelled"
			if (err != nil) != wantError || deleted != (change == "none") {
				t.Fatalf("conditional result: deleted=%v err=%v", deleted, err)
			}
			ctx = context.Background()
			_, getErr := s.GetResource(ctx, current.ID())
			var absent graph.ErrNotFound
			if errors.As(getErr, &absent) != (change == "none") {
				t.Fatalf("wrong retained row: %v", getErr)
			}
			edges, err := s.ListEdges(ctx, target.ID(), graph.DirectionIncoming)
			if err != nil {
				t.Fatal(err)
			}
			wantEdges := 1
			if change == "none" {
				wantEdges = 0
			}
			if len(edges) != wantEdges {
				t.Fatalf("incident edges=%d want=%d", len(edges), wantEdges)
			}
			if _, err := s.GetResource(ctx, target.ID()); err != nil {
				t.Fatal("conditional cascade removed target")
			}
			if change == "none" {
				if again, err := d.DeleteResourceVersion(ctx, old); again || err != nil {
					t.Fatal("delete not idempotent")
				}
			}
		})
	}
}
