// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestImpactReferences_EnvironmentFieldsAndOptionality(t *testing.T) {
	source := referenceSource(t, "Pod", "v1", `{
	  "containers":[{"env":[
	    {"name":"IGNORED","value":"literal-canary"},
	    {"valueFrom":{"configMapKeyRef":{"name":"settings","key":"key-canary"}}},
	    {"valueFrom":{"secretKeyRef":{"name":"credentials","optional":true,"key":"key-canary"}}}
	  ],"envFrom":[{"configMapRef":{"name":"settings","optional":false}}],
	  "command":["command-canary"],"args":["argument-canary"]}],
	  "initContainers":[{"envFrom":[{"configMapRef":{"name":"settings","optional":true}}]}],
	  "ephemeralContainers":[{"envFrom":[{"secretRef":{"name":"credentials"}}]}]
	}`)
	config := referenceEvidence(t, source, "ConfigMap", "settings")
	want := []analysis.ImpactReferenceUse{
		{FieldPath: "/spec/containers/0/envFrom/0/configMapRef", Mode: "environment", Optional: "required", Refresh: "container_restart_required"},
		{FieldPath: "/spec/containers/0/env/1/valueFrom/configMapKeyRef", Mode: "environment", Optional: "required", Refresh: "container_restart_required"},
		{FieldPath: "/spec/initContainers/0/envFrom/0/configMapRef", Mode: "environment", Optional: "optional", Refresh: "container_restart_required"},
	}
	if config.Status != "version_bound" || config.SourceUID != "consumer-uid" || config.SourceResourceVersion != "17" || !reflect.DeepEqual(config.Uses, want) {
		t.Fatalf("same edge lost individual source fields/flags: %+v", config)
	}
	secret := referenceEvidence(t, source, "Secret", "credentials")
	if secret.Status != "version_bound" || len(secret.Uses) != 2 || secret.Uses[0].Optional != "optional" || secret.Uses[1].Optional != "required" || secret.Uses[1].FieldPath != "/spec/ephemeralContainers/0/envFrom/0/secretRef" {
		t.Fatalf("Secret reference interpretation = %+v", secret)
	}
	for _, evidence := range []analysis.ImpactReferenceEvidence{config, secret} {
		encoded, err := json.Marshal(evidence)
		if err != nil || strings.Contains(string(encoded), "canary") {
			t.Fatalf("source values escaped the evidence allowlist: %v", err)
		}
	}
}

func TestImpactReferences_VolumeMountsProjectedSubPathAndPVC(t *testing.T) {
	source := referenceSource(t, "Pod", "v1", `{
	  "containers":[{"volumeMounts":[
	    {"name":"mixed","mountPath":"mount-canary"},
	    {"name":"mixed","subPath":"subpath-canary"},
	    {"name":"credentials","subPathExpr":"expr-canary"},
	    {"name":"claim"}
	  ]}],
	  "initContainers":[{"volumeMounts":[{"name":"mixed"}]}],
	  "volumes":[
	    {"name":"mixed","projected":{"sources":[
	      {"configMap":{"name":"settings","optional":true,"items":[{"key":"key-canary","path":"path-canary"}]}},
	      {"secret":{"name":"credentials"}},
	      {"serviceAccountToken":{"path":"token-path-canary"}}
	    ]}},
	    {"name":"credentials","secret":{"secretName":"credentials","optional":false}},
	    {"name":"claim","persistentVolumeClaim":{"claimName":"data"}},
	    {"name":"unmounted","configMap":{"name":"settings"}}
	  ]
	}`)
	config := referenceEvidence(t, source, "ConfigMap", "settings")
	want := []analysis.ImpactReferenceUse{
		{FieldPath: "/spec/volumes/0/projected/sources/0/configMap", MountFieldPath: "/spec/containers/0/volumeMounts/0", Mode: "volume_mount", Optional: "optional", Refresh: "eventual_projection"},
		{FieldPath: "/spec/volumes/0/projected/sources/0/configMap", MountFieldPath: "/spec/containers/0/volumeMounts/1", Mode: "volume_subpath", Optional: "optional", Refresh: "no_automatic_updates"},
		{FieldPath: "/spec/volumes/0/projected/sources/0/configMap", MountFieldPath: "/spec/initContainers/0/volumeMounts/0", Mode: "volume_mount", Optional: "optional", Refresh: "eventual_projection"},
		{FieldPath: "/spec/volumes/3/configMap", Mode: "volume_declaration", Optional: "required", Refresh: "unknown"},
	}
	if config.Status != "version_bound" || !reflect.DeepEqual(config.Uses, want) {
		t.Fatalf("volume semantics collapsed into a single edge: %+v", config)
	}
	secret := referenceEvidence(t, source, "Secret", "credentials")
	if secret.Status != "version_bound" || len(secret.Uses) != 4 || secret.Uses[0].Refresh != "eventual_projection" || secret.Uses[1].Refresh != "no_automatic_updates" || secret.Uses[3].Mode != "volume_subpath" || secret.Uses[3].Refresh != "no_automatic_updates" {
		t.Fatalf("Secret projected/regular/subPathExpr semantics = %+v", secret)
	}
	pvc := referenceEvidence(t, source, "PersistentVolumeClaim", "data")
	if pvc.Status != "version_bound" || !reflect.DeepEqual(pvc.Uses, []analysis.ImpactReferenceUse{{
		FieldPath: "/spec/volumes/2/persistentVolumeClaim", MountFieldPath: "/spec/containers/0/volumeMounts/3",
		Mode: "volume_mount", Optional: "not_applicable", Refresh: "storage_dependent",
	}}) {
		t.Fatalf("PVC incorrectly borrowed configuration refresh semantics: %+v", pvc)
	}
	body, err := json.Marshal([]analysis.ImpactReferenceEvidence{config, secret, pvc})
	if err != nil || strings.Contains(string(body), "canary") {
		t.Fatalf("volume contents/paths escaped projection: %v", err)
	}
}

func TestImpactReferences_BuiltinSourceSchemas(t *testing.T) {
	for _, tc := range []struct{ kind, version, prefix string }{
		{"Pod", "v1", "/spec"},
		{"Deployment", "apps/v1", "/spec/template/spec"},
		{"ReplicaSet", "apps/v1", "/spec/template/spec"},
		{"StatefulSet", "apps/v1", "/spec/template/spec"},
		{"DaemonSet", "apps/v1", "/spec/template/spec"},
		{"Job", "batch/v1", "/spec/template/spec"},
		{"CronJob", "batch/v1", "/spec/jobTemplate/spec/template/spec"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			source := referenceSource(t, tc.kind, tc.version, `{"containers":[{"envFrom":[{"configMapRef":{"name":"settings"}}]}]}`)
			got := referenceEvidence(t, source, "ConfigMap", "settings")
			if got.Status != "version_bound" || len(got.Uses) != 1 || got.Uses[0].FieldPath != tc.prefix+"/containers/0/envFrom/0/configMapRef" {
				t.Fatalf("schema/path interpretation = %+v", got)
			}
		})
	}
}

func TestImpactReferences_MissingOrStaleEvidenceStaysUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		change       func(*graph.Resource)
	}{
		{"missing UID", "missing_source_version", func(r *graph.Resource) { r.UID = "" }},
		{"missing version", "missing_source_version", func(r *graph.Resource) { r.ResourceVersion = "" }},
		{"identity projection only", "source_fields_unavailable", func(r *graph.Resource) { r.Raw = nil }},
		{"missing spec", "source_fields_unavailable", func(r *graph.Resource) { delete(r.Raw, "spec") }},
		{"old source version", "source_identity_or_version_mismatch", func(r *graph.Resource) { r.ResourceVersion = "18" }},
		{"recreated source", "source_identity_or_version_mismatch", func(r *graph.Resource) { r.UID = "replacement-uid" }},
		{"wrong source kind", "source_identity_or_version_mismatch", func(r *graph.Resource) { r.Raw["kind"] = "Deployment" }},
		{"wrong raw API version", "source_identity_or_version_mismatch", func(r *graph.Resource) { r.Raw["apiVersion"] = "custom/v1" }},
		{"wrong source namespace", "source_identity_or_version_mismatch", func(r *graph.Resource) { r.Namespace = "other" }},
		{"wrong source name", "source_identity_or_version_mismatch", func(r *graph.Resource) { r.Name = "other" }},
		{"custom Pod kind", "unsupported_source_schema", func(r *graph.Resource) { r.GroupVersion = "custom/v1"; r.Raw["apiVersion"] = "custom/v1" }},
		{"legacy source schema", "unsupported_source_schema", func(r *graph.Resource) { r.GroupVersion = "v1beta1" }},
		{"unmodeled Secret reference", "reference_not_found_or_not_modeled", func(r *graph.Resource) {
			r.Raw["spec"] = map[string]any{"imagePullSecrets": []any{map[string]any{"name": "settings"}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := referenceSource(t, "Pod", "v1", `{"containers":[{"envFrom":[{"configMapRef":{"name":"settings"}}]}]}`)
			tc.change(&source)
			got := referenceEvidence(t, source, "ConfigMap", "settings")
			if got.Status != "unknown" || got.Reason != tc.reason || len(got.Uses) != 0 {
				t.Fatalf("unbound evidence became a definitive explanation: %+v", got)
			}
		})
	}
}

func TestImpactReferences_MalformedFieldsDoNotInventDefaults(t *testing.T) {
	for _, spec := range []string{
		`{"containers":"malformed"}`,
		`{"containers":[null]}`,
		`{"containers":[{"envFrom":[null]}]}`,
		`{"containers":[{"envFrom":[{"configMapRef":null}]}]}`,
		`{"containers":[{"envFrom":[{"configMapRef":{"name":"settings","optional":"false"}}]}]}`,
		`{"containers":[{"envFrom":[{"configMapRef":{"name":"settings","optional":null}}]}]}`,
		`{"containers":[{"envFrom":[{"configMapRef":{"name":7}}]}]}`,
		`{"containers":[{"envFrom":[{"configMapRef":{"name":"other/settings"}}]}]}`,
		`{"containers":[{"env":[{"valueFrom":null}]}]}`,
		`{"containers":[{"volumeMounts":[{"name":"config","subPath":true}]}],"volumes":[{"name":"config","configMap":{"name":"settings"}}]}`,
		`{"volumes":[{"name":"config","configMap":{"name":"settings"}},{"name":"config","configMap":{"name":"settings"}}]}`,
		`{"volumes":[{"name":"config","projected":{"sources":{}}}]}`,
	} {
		source := referenceSource(t, "Pod", "v1", spec)
		got := referenceEvidence(t, source, "ConfigMap", "settings")
		if got.Status != "unknown" || got.Reason != "malformed_reference_fields" || len(got.Uses) != 0 {
			t.Fatalf("malformed source %s produced %+v", spec, got)
		}
	}
}

func TestImpactReferences_DoNotSynthesizeEdgesAndKeepConsumerDirection(t *testing.T) {
	source := referenceSource(t, "Pod", "v1", `{"containers":[{"envFrom":[{"configMapRef":{"name":"settings"}}]}]}`)
	projection := referenceGraph(source, "ConfigMap", "settings")
	before, err := json.Marshal(source.Raw)
	if err != nil {
		t.Fatal(err)
	}
	forward := requireImpact(t, projection, source.ID(), analysis.ImpactOptions{ClusterID: "prod", Relation: analysis.ImpactDependencies})
	reverse := requireImpact(t, projection, projection.Resources[1].ID(), analysis.ImpactOptions{ClusterID: "prod"})
	if !reflect.DeepEqual(forward.ReferenceEvidence, reverse.ReferenceEvidence) || len(forward.ReferenceEvidence) != 1 {
		t.Fatalf("dependents explanation used the target instead of consumer: %+v", reverse)
	}
	forward.ReferenceEvidence[0].Uses[0].FieldPath = "changed-output"
	if reverse.ReferenceEvidence[0].Uses[0].FieldPath == "changed-output" {
		t.Fatal("results alias mutable evidence slices")
	}
	after, err := json.Marshal(source.Raw)
	if err != nil || string(before) != string(after) {
		t.Fatal("analysis mutated the caller's source")
	}
	projection.Edges = nil
	noEdges := requireImpact(t, projection, source.ID(), analysis.ImpactOptions{ClusterID: "prod", Relation: analysis.ImpactDependencies})
	if noEdges.Counts.Total != 0 || len(noEdges.ReferenceEvidence) != 0 {
		t.Fatal("source interpretation introduced an unstored edge")
	}
	// An existing edge to another namespace is not justified by a local Pod ref.
	projection = referenceGraph(source, "ConfigMap", "settings")
	projection.Resources[1].Namespace = "other"
	projection.Edges[0].To = projection.Resources[1].ID()
	other := requireImpact(t, projection, source.ID(), analysis.ImpactOptions{ClusterID: "prod", Relation: analysis.ImpactDependencies})
	if other.ReferenceEvidence[0].Status != "unknown" || len(other.ReferenceEvidence[0].Uses) != 0 {
		t.Fatal("local reference justified a cross-namespace edge")
	}
}

func TestImpactReferences_BoundsAndMidScanCancellation(t *testing.T) {
	for _, tc := range []string{"entries", "bytes", "target identity bytes", "cancel"} {
		t.Run(tc, func(t *testing.T) {
			source := referenceSource(t, "Pod", "v1", `{}`)
			spec := source.Raw["spec"].(map[string]any)
			entries := make([]any, analysis.MaxImpactReferenceEntries+1)
			for i := range entries {
				entries[i] = map[string]any{} // Even ignored literal entries cost work.
			}
			spec["containers"] = []any{map[string]any{"env": entries}}
			ctx := context.Background()
			want := analysis.ErrImpactReferenceLimit
			if tc == "bytes" {
				spec["containers"] = []any{map[string]any{"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": strings.Repeat("x", analysis.MaxImpactReferenceBytes/6)}}}}}
			}
			if tc == "target identity bytes" {
				source.Namespace = strings.Repeat("n", 40_000)
				source.Raw["metadata"].(map[string]any)["namespace"] = source.Namespace
				refs := make([]any, 150)
				for i := range refs {
					refs[i] = map[string]any{"configMapRef": map[string]any{"name": "settings"}}
				}
				spec["containers"] = []any{map[string]any{"envFrom": refs}}
			}
			if tc == "cancel" {
				base, cancel := context.WithCancel(ctx)
				defer cancel()
				ctx = &impactCancelContext{Context: base, cancel: cancel, remaining: 150}
				want = context.Canceled
			}
			projection := referenceGraph(source, "ConfigMap", "settings")
			got, err := analysis.AnalyzeImpact(ctx, projection, source.ID(), analysis.ImpactOptions{ClusterID: "prod", Relation: analysis.ImpactDependencies})
			if got != nil || !errors.Is(err, want) {
				t.Fatalf("bounded failure returned partial success: %+v %v", got, err)
			}
		})
	}
}

func TestImpactReferences_StoreProjectionDoesNotFallBackToRaw(t *testing.T) {
	source := referenceSource(t, "Pod", "v1", `{"containers":[{"envFrom":[{"secretRef":{"name":"credentials"}}]}]}`)
	projection := referenceGraph(source, "Secret", "credentials")
	store := memory.New()
	ctx := context.Background()
	for _, resource := range projection.Resources {
		if err := store.UpsertResource(ctx, resource); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpsertEdge(ctx, projection.Edges[0]); err != nil {
		t.Fatal(err)
	}
	// An explicit identity-only provider must still stay unknown even when the
	// backing store has full source fields. Analysis may not fetch to fill gaps.
	projection.Resources[0].Raw = nil
	probe := &impactSnapshotProbe{GraphStore: store, read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) { return projection, nil }}
	result, err := analysis.AnalyzeStoredImpact(ctx, probe, source.ID(), analysis.ImpactOptions{ClusterID: "prod", Relation: analysis.ImpactDependencies}, graph.ImpactSnapshotOptions{ClusterID: "prod"})
	if err != nil || len(result.ReferenceEvidence) != 1 || result.ReferenceEvidence[0].Status != "unknown" || result.ReferenceEvidence[0].Reason != "source_fields_unavailable" || len(result.ReferenceEvidence[0].Uses) != 0 {
		t.Fatalf("identity-only projection silently loaded source fields: %+v %v", result, err)
	}
	if !result.Resources[0].Resource.ReferenceOnly || result.Resources[0].Resource.UID != "" || result.Resources[0].Resource.ResourceVersion != "" {
		t.Fatal("reference interpretation enriched a Secret identity")
	}
}

func TestImpactReferences_SecretSourceAndTargetSchema(t *testing.T) {
	source := referenceSource(t, "Pod", "v1", `{"containers":[{"envFrom":[{"configMapRef":{"name":"settings"}}]}]}`)
	projection := referenceGraph(source, "ConfigMap", "settings")
	projection.Resources[1].GroupVersion = "custom/v1"
	result := requireImpact(t, projection, source.ID(), analysis.ImpactOptions{ClusterID: "prod", Relation: analysis.ImpactDependencies})
	if result.ReferenceEvidence[0].Reason != "target_schema_mismatch" || len(result.ReferenceEvidence[0].Uses) != 0 {
		t.Fatal("core ConfigMap ref was attributed to a custom API group")
	}
	source.Kind = "Secret"
	source.UID, source.ResourceVersion = "secret-uid-canary", "secret-version-canary"
	source.Raw["data"] = map[string]any{"synthetic": "secret-value-canary"}
	got := referenceEvidence(t, source, "ConfigMap", "settings")
	body, err := json.Marshal(got)
	if err != nil || strings.Contains(string(body), "canary") || got.Reason != "reference_only_source" || got.Status != "unknown" || len(got.Uses) != 0 {
		t.Fatalf("Secret source crossed the reference-only boundary: %+v %v", got, err)
	}
}

func TestImpactReferences_ReusedSourceAndDistinctTargets(t *testing.T) {
	source := referenceSource(t, "Pod", "v1", `{
	  "containers":[{"envFrom":[
	    {"configMapRef":{"name":"a","optional":true}},
	    {"configMapRef":{"name":"b"}}
	  ],"volumeMounts":[{"name":"claim","subPath":"subpath-canary"}]}],
	  "volumes":[{"name":"claim","persistentVolumeClaim":{"claimName":"data"}}]
	}`)
	projection := referenceGraph(source, "ConfigMap", "a")
	second := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "b", ClusterID: "prod"}
	projection.Resources = append(projection.Resources, second)
	projection.Edges = append(projection.Edges, graph.Edge{From: source.ID(), To: second.ID(), Type: graph.EdgeTypeUsesConfigMap})
	result := requireImpact(t, projection, source.ID(), analysis.ImpactOptions{ClusterID: "prod", Relation: analysis.ImpactDependencies})
	if len(result.ReferenceEvidence) != 2 || result.ReferenceEvidence[0].Uses[0].Optional != "optional" || result.ReferenceEvidence[1].Uses[0].Optional != "required" || result.ReferenceEvidence[0].Uses[0].FieldPath == result.ReferenceEvidence[1].Uses[0].FieldPath {
		t.Fatalf("source cache reused the wrong target evidence: %+v", result.ReferenceEvidence)
	}
	result.ReferenceEvidence[0].Uses[0].Optional = "changed"
	if result.ReferenceEvidence[1].Uses[0].Optional != "required" {
		t.Fatal("distinct targets alias evidence")
	}
	pvc := referenceEvidence(t, source, "PersistentVolumeClaim", "data")
	if pvc.Uses[0].Mode != "volume_subpath" || pvc.Uses[0].Refresh != "storage_dependent" {
		t.Fatalf("PVC subPath falsely inherited Secret refresh behavior: %+v", pvc)
	}
}

func referenceEvidence(t *testing.T, source graph.Resource, kind, name string) analysis.ImpactReferenceEvidence {
	t.Helper()
	projection := referenceGraph(source, kind, name)
	result := requireImpact(t, projection, source.ID(), analysis.ImpactOptions{ClusterID: source.ClusterID, Relation: analysis.ImpactDependencies})
	if len(result.ReferenceEvidence) != 1 {
		t.Fatalf("expected one edge explanation: %+v", result)
	}
	return result.ReferenceEvidence[0]
}

func referenceGraph(source graph.Resource, kind, name string) *graph.Graph {
	target := graph.Resource{Kind: kind, Name: name, Namespace: source.Namespace, ClusterID: source.ClusterID}
	edgeType := graph.EdgeTypeUsesConfigMap
	switch kind {
	case "Secret":
		edgeType = graph.EdgeTypeUsesSecret
	case "PersistentVolumeClaim":
		edgeType = graph.EdgeTypeMountsVolume
	}
	return &graph.Graph{Resources: []graph.Resource{source, target}, Edges: []graph.Edge{{From: source.ID(), To: target.ID(), Type: edgeType}}}
}

func referenceSource(t *testing.T, kind, version, podSpec string) graph.Resource {
	t.Helper()
	var spec map[string]any
	if err := json.Unmarshal([]byte(podSpec), &spec); err != nil {
		t.Fatal(err)
	}
	source := graph.Resource{Kind: kind, Name: "consumer", Namespace: "demo", ClusterID: "prod", GroupVersion: version, UID: "consumer-uid", ResourceVersion: "17"}
	var outer map[string]any
	switch kind {
	case "Pod":
		outer = spec
	case "CronJob":
		outer = map[string]any{"jobTemplate": map[string]any{"spec": map[string]any{"template": map[string]any{"spec": spec}}}}
	default:
		outer = map[string]any{"template": map[string]any{"spec": spec}}
	}
	source.Raw = map[string]any{
		"kind": kind, "apiVersion": version, "spec": outer,
		"metadata": map[string]any{"uid": string(source.UID), "resourceVersion": source.ResourceVersion, "namespace": source.Namespace, "name": source.Name, "annotations": map[string]any{"test": "annotation-canary"}},
	}
	return source
}
