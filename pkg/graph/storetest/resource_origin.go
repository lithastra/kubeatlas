// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func ResourceOriginFixture(cluster string) graph.Resource {
	r := graph.Resource{ClusterID: cluster, Kind: "Foo", Namespace: "default", Name: "retained", GroupVersion: "example.com/v1", UID: "object-uid", ResourceVersion: "opaque-rv"}
	r.Origin = graph.ResourceOrigin{ClusterID: cluster, Source: "crd", OwnerName: "foos.example.com", OwnerUID: "private-owner-uid", Group: "example.com", Version: "v1", Resource: "foos", Kind: "Foo", Namespaced: true, Registration: strings.Repeat("a", 32), ObjectUID: string(r.UID), ObjectVersion: r.ResourceVersion}
	return r
}

// RunResourceOrigin exercises the same stored receipt semantics in both tiers.
func RunResourceOrigin(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("private receipt shares projection byte budget", func(t *testing.T) {
		s := factory(t)
		r := ResourceOriginFixture("east")
		origin := r.Origin
		r.Origin = graph.ResourceOrigin{}
		if err := s.UpsertResource(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		reader, ok := s.(graph.ImpactSnapshotter)
		if !ok {
			t.Fatal("store lacks impact capability")
		}
		limits := graph.ImpactSnapshotOptions{ClusterID: "east", MaxBytes: 1300}
		if _, err := reader.SnapshotImpact(context.Background(), limits); err != nil {
			t.Fatal("baseline did not fit", err)
		}
		r.Origin = origin
		if err := s.UpsertResource(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		if g, err := reader.SnapshotImpact(context.Background(), limits); !errors.Is(err, graph.ErrImpactSnapshotLimit) || g != nil {
			t.Fatal("private metadata bypassed projection budget", g, err)
		}
	})
	t.Run("private round trip and projections", func(t *testing.T) {
		ctx := context.Background()
		s := factory(t)
		r := ResourceOriginFixture("east")
		if err := s.UpsertResource(ctx, r); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetResource(ctx, r.ID())
		if err != nil || got.Origin != r.Origin {
			t.Fatal("source receipt lost on round trip", got.Origin, err)
		}
		reader, ok := s.(graph.ImpactSnapshotter)
		if !ok {
			t.Fatal("store lacks impact capability")
		}
		projection, err := reader.SnapshotImpact(ctx, graph.ImpactSnapshotOptions{ClusterID: "east"})
		if err != nil || len(projection.Resources) != 1 || projection.Resources[0].Origin != r.Origin {
			t.Fatal("impact projection lost private receipt", projection, err)
		}
		body, err := json.Marshal(projection)
		if err != nil || strings.Contains(string(body), r.Origin.OwnerUID) || strings.Contains(string(body), "observationOrigin") || strings.Contains(string(body), "registration") {
			t.Fatal("private receipt escaped resource JSON", string(body), err)
		}
		old := r
		r.Origin.Registration = strings.Repeat("b", 32)
		if err := s.UpsertResource(ctx, r); err != nil {
			t.Fatal(err)
		}
		d, ok := s.(graph.ResourceVersionDeleter)
		if !ok {
			t.Fatal("store lacks conditional deletion")
		}
		if deleted, err := d.DeleteResourceVersion(ctx, old); deleted || err != nil {
			t.Fatal("old source receipt deleted refreshed observation", deleted, err)
		}
		if deleted, err := d.DeleteResourceVersion(ctx, r); !deleted || err != nil {
			t.Fatal("current receipt could not delete", deleted, err)
		}
	})
	t.Run("missing invalid and Secret receipts", func(t *testing.T) {
		for _, change := range []string{"legacy", "foreign", "uid", "version", "scope", "secret"} {
			t.Run(change, func(t *testing.T) {
				s := factory(t)
				r := ResourceOriginFixture("east")
				switch change {
				case "legacy":
					r.Origin = graph.ResourceOrigin{}
				case "foreign":
					r.Origin.ClusterID = "west"
				case "uid":
					r.UID = "replacement"
				case "version":
					r.ResourceVersion = "replacement-rv"
				case "scope":
					r.Namespace = ""
				case "secret":
					r.Kind = "Secret"
				}
				if err := s.UpsertResource(context.Background(), r); err != nil {
					t.Fatal(err)
				}
				got, err := s.GetResource(context.Background(), r.ID())
				if err != nil || got.Origin != (graph.ResourceOrigin{}) {
					t.Fatal("invented receipt survived", got.Origin, err)
				}
			})
		}
	})
}
