// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/apimachinery/pkg/types"
)

func TestDetectOrphans_FindsOrphanReplicaSet(t *testing.T) {
	// A ReplicaSet that lost its Deployment AND has no Pods is the
	// canonical 0-incoming orphan. The strict "no incoming + not
	// top-level" rule the playbook prescribes catches this exact
	// shape; an RS that still has Pods owning it has incoming
	// edges and is not flagged (operationally still suspicious,
	// but outside the F-112 part 1 scope).
	s := memory.New()
	ctx := context.Background()
	orphanRS := graph.Resource{
		Kind: "ReplicaSet", Namespace: "demo", Name: "ghost-rs",
		UID: types.UID("rs-uid"),
	}
	_ = s.UpsertResource(ctx, orphanRS)

	got, err := analysis.DetectOrphans(ctx, s, analysis.OrphanOptions{})
	if err != nil {
		t.Fatalf("DetectOrphans: %v", err)
	}
	if !containsOrphan(got, "ghost-rs", analysis.ReasonOrphan) {
		t.Errorf("expected ghost-rs to be flagged as orphan, got %+v", got)
	}
}

func TestDetectOrphans_NamespaceIsNotOrphan(t *testing.T) {
	// Namespace is in the top-level whitelist; the detector must
	// never flag it even though it never has incoming edges.
	s := memory.New()
	ctx := context.Background()
	_ = s.UpsertResource(ctx, graph.Resource{Kind: "Namespace", Name: "demo"})

	got, err := analysis.DetectOrphans(ctx, s, analysis.OrphanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if r.Resource.Kind == "Namespace" {
			t.Errorf("Namespace must not be flagged as orphan, got %+v", r)
		}
	}
}

func TestDetectOrphans_TopLevelKindsAreNotOrphans(t *testing.T) {
	s := memory.New()
	ctx := context.Background()
	for _, kind := range []string{"Deployment", "Service", "ConfigMap", "Secret", "StatefulSet", "ServiceAccount"} {
		_ = s.UpsertResource(ctx, graph.Resource{Kind: kind, Namespace: "demo", Name: "x"})
	}
	got, err := analysis.DetectOrphans(ctx, s, analysis.OrphanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("top-level kinds must not be orphans, got %d reports: %+v", len(got), got)
	}
}

func TestDetectOrphans_StandalonePodFlagged(t *testing.T) {
	// `kubectl run` Pods have no OwnerReference. Detector flags them
	// with reason=standalone_pod, distinct from the orphan code.
	s := memory.New()
	ctx := context.Background()
	_ = s.UpsertResource(ctx, graph.Resource{
		Kind: "Pod", Namespace: "demo", Name: "lonely",
		UID: types.UID("pod-uid"),
	})
	got, err := analysis.DetectOrphans(ctx, s, analysis.OrphanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsOrphan(got, "lonely", analysis.ReasonStandalonePod) {
		t.Errorf("expected standalone Pod report, got %+v", got)
	}
}

func TestDetectOrphans_OwnedPodIsNotFlagged(t *testing.T) {
	// A Pod with OwnerReference + an actual incoming edge is normal.
	s := memory.New()
	ctx := context.Background()
	rs := graph.Resource{Kind: "ReplicaSet", Namespace: "demo", Name: "api-rs", UID: types.UID("rs-uid")}
	pod := graph.Resource{
		Kind: "Pod", Namespace: "demo", Name: "api-1",
		OwnerReferences: []graph.OwnerRef{{Kind: "ReplicaSet", Name: "api-rs", UID: types.UID("rs-uid")}},
	}
	dep := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "api"}
	_ = s.UpsertResource(ctx, dep)
	_ = s.UpsertResource(ctx, rs)
	_ = s.UpsertResource(ctx, pod)
	_ = s.UpsertEdge(ctx, graph.Edge{From: rs.ID(), To: dep.ID(), Type: graph.EdgeTypeOwns})
	_ = s.UpsertEdge(ctx, graph.Edge{From: pod.ID(), To: rs.ID(), Type: graph.EdgeTypeOwns})

	got, err := analysis.DetectOrphans(ctx, s, analysis.OrphanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if r.Resource.Name == "api-1" {
			t.Errorf("owned Pod must not be flagged, got %+v", r)
		}
	}
}

func TestDetectOrphans_NamespaceFilter(t *testing.T) {
	s := memory.New()
	ctx := context.Background()
	_ = s.UpsertResource(ctx, graph.Resource{Kind: "ReplicaSet", Namespace: "demo", Name: "rs-demo"})
	_ = s.UpsertResource(ctx, graph.Resource{Kind: "ReplicaSet", Namespace: "other", Name: "rs-other"})

	got, err := analysis.DetectOrphans(ctx, s, analysis.OrphanOptions{Namespace: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Resource.Name != "rs-demo" {
		t.Errorf("namespace filter not applied, got %+v", got)
	}
}

func TestDetectOrphans_EmptyStore(t *testing.T) {
	s := memory.New()
	got, err := analysis.DetectOrphans(context.Background(), s, analysis.OrphanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("empty store: got %d reports, want 0", len(got))
	}
}

func containsOrphan(reports []analysis.OrphanReport, name string, reason analysis.OrphanReason) bool {
	for _, r := range reports {
		if r.Resource.Name == name && r.Reason == reason {
			return true
		}
	}
	return false
}
