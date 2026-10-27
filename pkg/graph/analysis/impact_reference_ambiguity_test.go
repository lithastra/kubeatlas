// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

var referenceAmbiguitySchemas = []struct{ kind, version string }{
	{"Pod", "v1"}, {"Deployment", "apps/v1"}, {"ReplicaSet", "apps/v1"},
	{"StatefulSet", "apps/v1"}, {"DaemonSet", "apps/v1"}, {"Job", "batch/v1"}, {"CronJob", "batch/v1"},
}

func TestImpactReferencesAmbiguousVolumesRemainUnknown(t *testing.T) {
	for _, schema := range referenceAmbiguitySchemas {
		for _, cluster := range []string{"east", ""} {
			for _, relation := range []analysis.ImpactRelation{analysis.ImpactDependencies, analysis.ImpactDependents} {
				for _, mode := range []string{"config-secret", "config-claim", "config-projected", "secret-claim", "secret-projected", "claim-projected", "projected-source", "mount-paths"} {
					t.Run(fmt.Sprintf("%s/cluster=%s/%s/%s", schema.kind, cluster, relation, mode), func(t *testing.T) {
						spec := referenceAmbiguitySpec(mode)
						source := referenceSource(t, schema.kind, schema.version, spec)
						source.ClusterID = cluster
						g := referenceAmbiguityGraph(source)
						store := memory.New()
						for _, row := range g.Resources {
							if err := store.UpsertResource(context.Background(), row); err != nil {
								t.Fatal(err)
							}
						}
						for _, edge := range g.Edges {
							if err := store.UpsertEdge(context.Background(), edge); err != nil {
								t.Fatal(err)
							}
						}
						original, err := json.Marshal(source.Raw)
						if err != nil {
							t.Fatal(err)
						}
						root, wantCount := source.ID(), 3
						if relation == analysis.ImpactDependents {
							root, wantCount = g.Resources[1].ID(), 1
						}
						opts := analysis.ImpactOptions{ClusterID: cluster, Relation: relation}
						pure := requireImpact(t, g, root, opts)
						stored, err := analysis.AnalyzeStoredImpact(context.Background(), store, root, opts, graph.ImpactSnapshotOptions{ClusterID: cluster})
						if err != nil || !reflect.DeepEqual(stored, pure) {
							t.Fatal("stored projection changed ambiguity qualification", stored, pure, err)
						}
						if pure.Counts.Total != wantCount || len(pure.ReferenceEvidence) != wantCount {
							t.Fatal("ambiguity removed stored relationships or synthesized new ones", pure.Counts, pure.ReferenceEvidence)
						}
						for _, evidence := range pure.ReferenceEvidence {
							if evidence.Status != "unknown" || evidence.Reason != "malformed_reference_fields" || len(evidence.Uses) != 0 {
								t.Fatal("ambiguous volume justified definitive use/defaults", evidence)
							}
						}
						body, err := json.Marshal(stored)
						if err != nil || strings.Contains(string(body), "canary") {
							t.Fatal("source or Secret payload escaped qualification", string(body), err)
						}
						after, err := json.Marshal(source.Raw)
						retained, readErr := store.GetResource(context.Background(), source.ID())
						if err != nil || readErr != nil || string(after) != string(original) || !reflect.DeepEqual(retained.Raw, source.Raw) {
							t.Fatal("ambiguity handling rewrote retained source", err, readErr)
						}
						// Qualification is per captured source version. A later valid
						// version restores explanation without changing stored edges.
						fixed := referenceSource(t, schema.kind, schema.version, `{"containers":[{"envFrom":[{"configMapRef":{"name":"settings"}},{"secretRef":{"name":"credentials"}}]}],"volumes":[{"name":"claim","persistentVolumeClaim":{"claimName":"data"}}]}`)
						fixed.ClusterID, fixed.ResourceVersion = cluster, "18"
						fixed.Raw["metadata"].(map[string]any)["resourceVersion"] = "18"
						if err := store.UpsertResource(context.Background(), fixed); err != nil {
							t.Fatal(err)
						}
						recovered, err := analysis.AnalyzeStoredImpact(context.Background(), store, root, opts, graph.ImpactSnapshotOptions{ClusterID: cluster})
						if err != nil || recovered.Counts.Total != wantCount || len(recovered.ReferenceEvidence) != wantCount {
							t.Fatal("fresh source version changed stored traversal", recovered, err)
						}
						for _, evidence := range recovered.ReferenceEvidence {
							if evidence.Status != "version_bound" || evidence.SourceResourceVersion != "18" || len(evidence.Uses) == 0 {
								t.Fatal("prior ambiguity poisoned a new source version", evidence)
							}
						}
					})
				}
			}
		}
	}
}

func referenceAmbiguitySpec(mode string) string {
	sources := map[string]string{
		"config":    `"configMap":{"name":"settings"}`,
		"secret":    `"secret":{"secretName":"credentials"}`,
		"claim":     `"persistentVolumeClaim":{"claimName":"data"}`,
		"projected": `"projected":{"sources":[{"configMap":{"name":"settings"}}]}`,
	}
	volume := `"configMap":{"name":"settings"}`
	mount := `{"name":"ambiguous"}`
	switch mode {
	case "projected-source":
		volume = `"projected":{"sources":[{"configMap":{"name":"settings"},"secret":{"name":"credentials"}}]}`
	case "mount-paths":
		mount = `{"name":"ambiguous","subPath":"subpath-canary","subPathExpr":"expression-canary"}`
	default:
		parts := strings.Split(mode, "-")
		volume = sources[parts[0]] + "," + sources[parts[1]]
	}
	// Valid references elsewhere must not salvage definitive explanation from
	// a source containing an ambiguous volume or mount declaration.
	return `{"containers":[{"envFrom":[{"configMapRef":{"name":"settings"}},{"secretRef":{"name":"credentials"}}],"volumeMounts":[` + mount + `]}],"volumes":[{"name":"ambiguous",` + volume + `},{"name":"claim","persistentVolumeClaim":{"claimName":"data"}}]}`
}

func referenceAmbiguityGraph(source graph.Resource) *graph.Graph {
	g := &graph.Graph{Resources: []graph.Resource{source}}
	for _, target := range []struct {
		kind, name string
		edge       graph.EdgeType
	}{{"ConfigMap", "settings", graph.EdgeTypeUsesConfigMap}, {"Secret", "credentials", graph.EdgeTypeUsesSecret}, {"PersistentVolumeClaim", "data", graph.EdgeTypeMountsVolume}} {
		r := graph.Resource{ClusterID: source.ClusterID, Namespace: source.Namespace, Kind: target.kind, Name: target.name}
		g.Resources = append(g.Resources, r)
		g.Edges = append(g.Edges, graph.Edge{From: source.ID(), To: r.ID(), Type: target.edge})
	}
	return g
}

func TestImpactReferencesSeparateSourcesAndEmptyMountAlternativesRemainSupported(t *testing.T) {
	for _, schema := range referenceAmbiguitySchemas {
		for _, mode := range []string{"projected", "subpath", "expression", "both-empty"} {
			t.Run(schema.kind+"/"+mode, func(t *testing.T) {
				mount := `{"name":"config","subPath":"","subPathExpr":""}`
				wantMode, wantRefresh := "volume_mount", "eventual_projection"
				switch mode {
				case "subpath":
					mount = `{"name":"config","subPath":"path-canary","subPathExpr":""}`
					wantMode, wantRefresh = "volume_subpath", "no_automatic_updates"
				case "expression":
					mount = `{"name":"config","subPath":"","subPathExpr":"expression-canary"}`
					wantMode, wantRefresh = "volume_subpath", "no_automatic_updates"
				}
				volume := `"configMap":{"name":"settings"}`
				if mode == "projected" {
					volume = `"projected":{"sources":[{"configMap":{"name":"settings"}},{"secret":{"name":"credentials"}},{"serviceAccountToken":{"path":"token-canary"}}]}`
				}
				source := referenceSource(t, schema.kind, schema.version, `{"containers":[{"volumeMounts":[`+mount+`]}],"volumes":[{"name":"config",`+volume+`}]}`)
				g := referenceGraph(source, "ConfigMap", "settings")
				store := memory.New()
				for _, r := range g.Resources {
					if err := store.UpsertResource(context.Background(), r); err != nil {
						t.Fatal(err)
					}
				}
				if err := store.UpsertEdge(context.Background(), g.Edges[0]); err != nil {
					t.Fatal(err)
				}
				result, err := analysis.AnalyzeStoredImpact(context.Background(), store, source.ID(), analysis.ImpactOptions{ClusterID: source.ClusterID, Relation: analysis.ImpactDependencies}, graph.ImpactSnapshotOptions{ClusterID: source.ClusterID})
				if err != nil || len(result.ReferenceEvidence) != 1 {
					t.Fatal("valid reference lost after projection", result, err)
				}
				e := result.ReferenceEvidence[0]
				if e.Status != "version_bound" || len(e.Uses) != 1 || e.Uses[0].Mode != wantMode || e.Uses[0].Refresh != wantRefresh || e.Uses[0].Optional != "required" {
					t.Fatal("valid combination was treated as ambiguous", e)
				}
			})
		}
	}
}
