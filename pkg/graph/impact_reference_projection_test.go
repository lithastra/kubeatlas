// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
)

func TestImpactReferenceProjection_OwnershipIdempotenceAndLegacySerialization(t *testing.T) {
	source, expected := storetest.ImpactReferenceFixture(t, "Pod", "v1")
	before, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	project := func(r graph.Resource) graph.Resource {
		builder, err := graph.NewImpactProjectionBuilder(graph.ImpactSnapshotOptions{ClusterID: "prod"})
		if err != nil {
			t.Fatal(err)
		}
		if err := builder.AddResource(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		return builder.Graph.Resources[0]
	}
	actual := project(source)
	if !reflect.DeepEqual(actual, expected) || !reflect.DeepEqual(project(actual), actual) {
		t.Fatal("projection changed shape on second pass")
	}
	after, err := json.Marshal(actual)
	if err != nil || string(before) != string(after) {
		t.Fatal("internal reference projection altered Resource's public serialization")
	}
	actual.Raw["metadata"].(map[string]any)["uid"] = "changed"
	actual.Raw["spec"].(map[string]any)["containers"].([]any)[0] = nil
	if !reflect.DeepEqual(project(source), expected) {
		t.Fatal("returned nested evidence aliases original Raw")
	}
}

func TestImpactReferenceProjection_SchemaCannotBeMutatedThroughAccessors(t *testing.T) {
	shape := graph.ImpactReferenceShape("Pod", "v1")
	fields := shape.Fields()
	fields[0].Name = "private"
	*fields[0].Shape = graph.ImpactFieldShape{}
	*shape = graph.ImpactFieldShape{}
	again := graph.ImpactReferenceShape("Pod", "v1")
	if again.Kind() != "object" || again.Fields()[0].Name != "kind" || again.Fields()[0].Shape.Kind() != "string" {
		t.Fatal("shared projection allowlist is mutable")
	}
	containerArray := again.Fields()[3].Shape.Fields()[0].Shape
	*containerArray.Element() = graph.ImpactFieldShape{}
	if containerArray.Element().Kind() != "object" {
		t.Fatal("element accessor exposes shared state")
	}
	path := graph.ImpactPodSpecPath("CronJob", "batch/v1")
	path[0] = "private"
	if graph.ImpactPodSpecPath("CronJob", "batch/v1")[0] != "spec" {
		t.Fatal("shared source path is mutable")
	}
	for _, kind := range []string{"Secret", "ConfigMap", "Service", "Gateway"} {
		if graph.ImpactReferenceShape(kind, "v1") != nil {
			t.Fatalf("unexpected payload projection for %s", kind)
		}
	}
}

func TestImpactReferenceProjection_CancellationAndBudgets(t *testing.T) {
	for _, scenario := range []string{"cancel", "array budget", "escaped string budget", "ignored payload"} {
		t.Run(scenario, func(t *testing.T) {
			source, _ := storetest.ImpactReferenceFixture(t, "Pod", "v1")
			spec := source.Raw["spec"].(map[string]any)
			ctx := context.Background()
			want := graph.ErrImpactSnapshotLimit
			budget := 65536
			switch scenario {
			case "cancel":
				base, cancel := context.WithCancel(ctx)
				defer cancel()
				ctx = &projectionCancelContext{Context: base, cancel: cancel, remaining: 15}
				want = context.Canceled
			case "array budget":
				items := make([]any, 3000)
				for i := range items {
					items[i] = map[string]any{"value": "ignored-canary"}
				}
				spec["containers"].([]any)[0].(map[string]any)["env"] = items
			case "escaped string budget":
				source.Raw["metadata"].(map[string]any)["uid"] = strings.Repeat("\x00", 20000)
			case "ignored payload":
				source.Raw["data"] = strings.Repeat("payload-canary", 50000)
				want = nil
			}
			builder, err := graph.NewImpactProjectionBuilder(graph.ImpactSnapshotOptions{ClusterID: "prod", MaxBytes: budget})
			if err != nil {
				t.Fatal(err)
			}
			err = builder.AddResource(ctx, source)
			if !errors.Is(err, want) || want != nil && len(builder.Graph.Resources) != 0 {
				t.Fatalf("partial or unbounded reference read: %v", err)
			}
		})
	}
}

type projectionCancelContext struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *projectionCancelContext) Err() error {
	c.remaining--
	if c.remaining == 0 {
		c.cancel()
	}
	return c.Context.Err()
}
