// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
)

func runImpactReferenceProjection(t *testing.T, factory Factory) {
	ctx := context.Background()
	for _, schema := range []struct{ kind, version string }{
		{"Pod", "v1"}, {"Deployment", "apps/v1"}, {"ReplicaSet", "apps/v1"},
		{"DaemonSet", "apps/v1"}, {"StatefulSet", "apps/v1"}, {"Job", "batch/v1"}, {"CronJob", "batch/v1"},
	} {
		t.Run(schema.kind, func(t *testing.T) {
			s := factory(t)
			source, expected := ImpactReferenceFixture(t, schema.kind, schema.version)
			g := seedReferenceGraph(t, s, source)
			opts := analysis.ImpactOptions{ClusterID: "prod", Relation: analysis.ImpactDependencies}
			limits := graph.ImpactSnapshotOptions{ClusterID: "prod"}
			pure, err := analysis.AnalyzeImpact(ctx, g, source.ID(), opts)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := analysis.AnalyzeStoredImpact(ctx, s, source.ID(), opts, limits)
			if err != nil || !reflect.DeepEqual(actual, pure) {
				t.Fatalf("stored interpretation differs from source: %+v %v", actual, err)
			}
			if len(actual.ReferenceEvidence) != 3 || actual.Counts.Total != 3 {
				t.Fatalf("missing reference types: %+v", actual)
			}
			for _, evidence := range actual.ReferenceEvidence {
				if evidence.Status != "version_bound" || evidence.SourceUID != "consumer-uid" || evidence.SourceResourceVersion != "17" || len(evidence.Uses) == 0 {
					t.Fatalf("unbound evidence: %+v", evidence)
				}
			}
			reader := requireImpactReader(t, s)
			projected, err := reader.SnapshotImpact(ctx, limits)
			if err != nil {
				t.Fatal(err)
			}
			for _, resource := range projected.Resources {
				if resource.ID() == source.ID() {
					if !reflect.DeepEqual(resource.Raw, expected.Raw) {
						t.Fatalf("reference allowlist differs from independent fixture: %+v", resource.Raw)
					}
					// Altering nested returned fields cannot alter the live store.
					fixtureMap(resource.Raw["metadata"])["resourceVersion"] = "modified"
					fixtureMap(resource.Raw["spec"])["modified"] = true
				} else if resource.Raw != nil || resource.Kind == "Secret" && (resource.UID != "" || resource.ResourceVersion != "") {
					t.Fatal("target payload or Secret identity leaked into projection")
				}
			}
			again, err := analysis.AnalyzeStoredImpact(ctx, s, source.ID(), opts, limits)
			if err != nil || !reflect.DeepEqual(again, pure) {
				t.Fatal("returned source fields alias the store")
			}
			stored, err := s.GetResource(ctx, source.ID())
			if err != nil || !reflect.DeepEqual(stored.Raw, source.Raw) {
				t.Fatal("projection rewrote the non-Secret source")
			}
			body, err := json.Marshal(actual)
			if err != nil || strings.Contains(string(body), "canary") {
				t.Fatal("unrelated source fields escaped analysis result")
			}
		})
	}
	for _, tc := range []struct {
		name, reason string
		change       func(*graph.Resource)
	}{
		{"version mismatch", "source_identity_or_version_mismatch", func(r *graph.Resource) { r.ResourceVersion = "18" }},
		{"UID mismatch", "source_identity_or_version_mismatch", func(r *graph.Resource) { r.UID = "replacement" }},
		{"missing version", "missing_source_version", func(r *graph.Resource) { r.ResourceVersion = "" }},
		{"no raw", "source_fields_unavailable", func(r *graph.Resource) { r.Raw = nil }},
		{"custom schema", "unsupported_source_schema", func(r *graph.Resource) { r.GroupVersion = "custom/v1" }},
		{"null optional", "malformed_reference_fields", func(r *graph.Resource) { setFixtureOptional(r, nil) }},
		{"object optional", "malformed_reference_fields", func(r *graph.Resource) { setFixtureOptional(r, map[string]any{"payload": "nested-canary"}) }},
		{"malformed array", "malformed_reference_fields", func(r *graph.Resource) {
			fixtureMap(r.Raw["spec"])["initContainers"] = map[string]any{"payload": "array-canary"}
		}},
		{"malformed array item", "malformed_reference_fields", func(r *graph.Resource) { fixtureMap(r.Raw["spec"])["initContainers"] = []any{"array-item-canary"} }},
		{"malformed mount flag", "malformed_reference_fields", func(r *graph.Resource) {
			containers := fixtureList(fixtureMap(r.Raw["spec"])["containers"])
			mounts := fixtureList(fixtureMap(containers[0])["volumeMounts"])
			fixtureMap(mounts[0])["subPath"] = map[string]any{"payload": "path-canary"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := factory(t)
			source, _ := ImpactReferenceFixture(t, "Pod", "v1")
			tc.change(&source)
			seedReferenceGraph(t, s, source)
			got, err := analysis.AnalyzeStoredImpact(ctx, s, source.ID(), analysis.ImpactOptions{ClusterID: "prod", Relation: analysis.ImpactDependencies}, graph.ImpactSnapshotOptions{ClusterID: "prod"})
			if err != nil {
				t.Fatal(err)
			}
			for _, evidence := range got.ReferenceEvidence {
				if evidence.Status != "unknown" || evidence.Reason != tc.reason || len(evidence.Uses) != 0 {
					t.Fatalf("bad source became a definitive/default explanation: %+v", evidence)
				}
			}
		})
	}
	t.Run("reference fields share total byte budget", func(t *testing.T) {
		s := factory(t)
		source, _ := ImpactReferenceFixture(t, "Pod", "v1")
		if err := s.UpsertResource(ctx, source); err != nil {
			t.Fatal(err)
		}
		reader := requireImpactReader(t, s)
		got, err := reader.SnapshotImpact(ctx, graph.ImpactSnapshotOptions{ClusterID: "prod", MaxBytes: 1024})
		if got != nil || !errors.Is(err, graph.ErrImpactSnapshotLimit) {
			t.Fatalf("reference projection ignored budget: %+v %v", got, err)
		}
		if _, err := reader.SnapshotImpact(ctx, graph.ImpactSnapshotOptions{ClusterID: "prod"}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("PVC has no optional lookup field", func(t *testing.T) {
		s := factory(t)
		source, _ := ImpactReferenceFixture(t, "Pod", "v1")
		volumes := fixtureList(fixtureMap(source.Raw["spec"])["volumes"])
		// Unknown fields are not part of the Kubernetes reference schema. The
		// interpreter must ignore this on PVC just as the allowlist does.
		fixtureMap(fixtureMap(volumes[3])["persistentVolumeClaim"])["optional"] = map[string]any{"ignored": "optional-canary"}
		g := seedReferenceGraph(t, s, source)
		opts := analysis.ImpactOptions{ClusterID: "prod", Relation: analysis.ImpactDependencies}
		pure, err := analysis.AnalyzeImpact(ctx, g, source.ID(), opts)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := analysis.AnalyzeStoredImpact(ctx, s, source.ID(), opts, graph.ImpactSnapshotOptions{ClusterID: "prod"})
		if err != nil || !reflect.DeepEqual(actual, pure) {
			t.Fatalf("unknown PVC field changed interpretation across tiers: %v", err)
		}
		for _, evidence := range actual.ReferenceEvidence {
			if evidence.Status != "version_bound" {
				t.Fatal("irrelevant field poisoned valid reference evidence")
			}
			if evidence.Type == graph.EdgeTypeMountsVolume && evidence.Uses[0].Optional != "not_applicable" {
				t.Fatal("PVC inherited an optional lookup default")
			}
		}
	})
}

func setFixtureOptional(r *graph.Resource, value any) {
	containers := fixtureList(fixtureMap(r.Raw["spec"])["containers"])
	refs := fixtureList(fixtureMap(containers[0])["envFrom"])
	fixtureMap(fixtureMap(refs[0])["configMapRef"])["optional"] = value
}

// These helpers assert the hand-authored test fixture's structure, not a source
// object's validity. Production projection code uses checked, fail-closed reads.
func fixtureMap(value any) map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		panic("storetest: invalid synthetic object fixture")
	}
	return object
}

func fixtureList(value any) []any {
	items, ok := value.([]any)
	if !ok {
		panic("storetest: invalid synthetic array fixture")
	}
	return items
}

func seedReferenceGraph(t *testing.T, s graph.GraphStore, source graph.Resource) *graph.Graph {
	t.Helper()
	g := &graph.Graph{Resources: []graph.Resource{source}}
	for _, target := range []struct {
		kind, name string
		edge       graph.EdgeType
	}{
		{"ConfigMap", "settings", graph.EdgeTypeUsesConfigMap}, {"Secret", "credentials", graph.EdgeTypeUsesSecret}, {"PersistentVolumeClaim", "data", graph.EdgeTypeMountsVolume},
	} {
		r := graph.Resource{Kind: target.kind, Name: target.name, Namespace: source.Namespace, ClusterID: source.ClusterID, Raw: map[string]any{"data": "target-canary"}}
		g.Resources = append(g.Resources, r)
		g.Edges = append(g.Edges, graph.Edge{From: source.ID(), To: r.ID(), Type: target.edge})
	}
	for _, r := range g.Resources {
		if err := s.UpsertResource(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range g.Edges {
		if err := s.UpsertEdge(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

// ImpactReferenceFixture supplies a synthetic full consumer and an independent
// allowlisted oracle. It is shared with PostgreSQL's raw-wire boundary tests.
func ImpactReferenceFixture(t *testing.T, kind, version string) (graph.Resource, graph.Resource) {
	t.Helper()
	full := `{
	  "containers":[{"name":"container-canary","args":["argument-canary"],
	    "env":[{"name":"literal-canary","value":"value-canary"},{"valueFrom":{"configMapKeyRef":{"name":"settings","key":"key-canary"}}}],
	    "envFrom":[{"prefix":"prefix-canary","configMapRef":{"name":"settings","optional":true}},{"secretRef":{"name":"credentials","optional":false}}],
	    "volumeMounts":[{"name":"config","mountPath":"mount-canary","subPath":""},{"name":"secret","subPathExpr":"expression-canary"},{"name":"projected"},{"name":"claim"}]
	  }],
	  "initContainers":[{"envFrom":[{"configMapRef":{"name":"settings"}}]}],
	  "ephemeralContainers":[{"envFrom":[{"secretRef":{"name":"credentials","optional":true}}]}],
	  "volumes":[
	    {"name":"config","configMap":{"name":"settings","items":[{"key":"key-canary","path":"path-canary"}]}},
	    {"name":"secret","secret":{"secretName":"credentials","optional":true}},
	    {"name":"projected","projected":{"sources":[{"configMap":{"name":"settings","optional":false}},{"secret":{"name":"credentials"}},{"serviceAccountToken":{"path":"token-path-canary"}}]}},
	    {"name":"claim","persistentVolumeClaim":{"claimName":"data","readOnly":true}}
	  ],"imagePullSecrets":[{"name":"image-canary"}]
	}`
	projected := `{
	  "containers":[{
	    "env":[{},{"valueFrom":{"configMapKeyRef":{"name":"settings"}}}],
	    "envFrom":[{"configMapRef":{"name":"settings","optional":true}},{"secretRef":{"name":"credentials","optional":false}}],
	    "volumeMounts":[{"name":"config","subPath":""},{"name":"secret","subPathExpr":"<present>"},{"name":"projected"},{"name":"claim"}]
	  }],
	  "initContainers":[{"envFrom":[{"configMapRef":{"name":"settings"}}]}],
	  "ephemeralContainers":[{"envFrom":[{"secretRef":{"name":"credentials","optional":true}}]}],
	  "volumes":[
	    {"name":"config","configMap":{"name":"settings"}},
	    {"name":"secret","secret":{"secretName":"credentials","optional":true}},
	    {"name":"projected","projected":{"sources":[{"configMap":{"name":"settings","optional":false}},{"secret":{"name":"credentials"}},{}]}},
	    {"name":"claim","persistentVolumeClaim":{"claimName":"data"}}
	  ]
	}`
	makeResource := func(specJSON string) graph.Resource {
		var spec map[string]any
		if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
			t.Fatal(err)
		}
		switch kind {
		case "Pod":
		case "CronJob":
			spec = map[string]any{"jobTemplate": map[string]any{"spec": map[string]any{"template": map[string]any{"spec": spec}}}}
		default:
			spec = map[string]any{"template": map[string]any{"spec": spec}}
		}
		return graph.Resource{Kind: kind, Name: "consumer", Namespace: "demo", ClusterID: "prod", GroupVersion: version, UID: "consumer-uid", ResourceVersion: "17", Raw: map[string]any{
			"kind": kind, "apiVersion": version, "spec": spec,
			"metadata": map[string]any{"name": "consumer", "namespace": "demo", "uid": "consumer-uid", "resourceVersion": "17"},
		}}
	}
	source, expected := makeResource(full), makeResource(projected)
	fixtureMap(source.Raw["metadata"])["annotations"] = map[string]any{"last-applied": "annotation-canary"}
	source.Raw["status"] = map[string]any{"ignored": "status-canary"}
	return source, expected
}
