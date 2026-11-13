// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestImpactReferencesAmbiguousEnvironmentRemainsUnknown(t *testing.T) {
	for _, schema := range referenceAmbiguitySchemas {
		for _, cluster := range []string{"east", ""} {
			for _, relation := range []analysis.ImpactRelation{analysis.ImpactDependencies, analysis.ImpactDependents} {
				for _, field := range []string{"containers", "initContainers", "ephemeralContainers"} {
					for _, mode := range []string{"envFrom", "env"} {
						t.Run(fmt.Sprintf("%s/cluster=%s/%s/%s/%s", schema.kind, cluster, relation, field, mode), func(t *testing.T) {
							entry := `"envFrom":[{"configMapRef":{"name":"settings","optional":false},"secretRef":{"name":"credentials","optional":true}}]`
							if mode == "env" {
								entry = `"env":[{"valueFrom":{"configMapKeyRef":{"name":"settings","key":"config-key-canary"},"secretKeyRef":{"name":"credentials","key":"secret-key-canary","optional":true}}}]`
							}
							// Valid volume references cannot salvage a definitive
							// explanation from an ambiguous consumer declaration.
							spec := `{"` + field + `":[{` + entry + `}],"volumes":[{"name":"config","configMap":{"name":"settings"}},{"name":"secret","secret":{"secretName":"credentials"}},{"name":"claim","persistentVolumeClaim":{"claimName":"data"}}]}`
							assertReferenceAmbiguity(t, schema.kind, schema.version, cluster, relation, spec)
						})
					}
				}
			}
		}
	}
}

func TestImpactReferencesSeparateEnvironmentSourcesRemainSupported(t *testing.T) {
	for _, schema := range referenceAmbiguitySchemas {
		for _, field := range []string{"containers", "initContainers", "ephemeralContainers"} {
			for _, mode := range []string{"envFrom", "env"} {
				t.Run(schema.kind+"/"+field+"/"+mode, func(t *testing.T) {
					entries := `"envFrom":[{"prefix":"prefix-canary","configMapRef":{"name":"settings","optional":false}},{"secretRef":{"name":"credentials","optional":true}}]`
					if mode == "env" {
						entries = `"env":[{"name":"literal-name-canary","value":"literal-value-canary"},{"valueFrom":{"fieldRef":{"fieldPath":"metadata.name"}}},{"valueFrom":{"configMapKeyRef":{"name":"settings","key":"config-key-canary","optional":false}}},{"valueFrom":{"secretKeyRef":{"name":"credentials","key":"secret-key-canary","optional":true}}}]`
					}
					source := referenceSource(t, schema.kind, schema.version, `{"`+field+`":[{`+entries+`}],"volumes":[{"name":"claim","persistentVolumeClaim":{"claimName":"data"}}]}`)
					g := referenceAmbiguityGraph(source)
					store := memory.New()
					for _, r := range g.Resources {
						if err := store.UpsertResource(context.Background(), r); err != nil {
							t.Fatal(err)
						}
					}
					for _, e := range g.Edges {
						if err := store.UpsertEdge(context.Background(), e); err != nil {
							t.Fatal(err)
						}
					}
					result, err := analysis.AnalyzeStoredImpact(context.Background(), store, source.ID(), analysis.ImpactOptions{ClusterID: source.ClusterID, Relation: analysis.ImpactDependencies}, graph.ImpactSnapshotOptions{ClusterID: source.ClusterID})
					if err != nil || result.Counts.Total != 3 || len(result.ReferenceEvidence) != 3 {
						t.Fatal("separate sources lost stored references", result, err)
					}
					for _, e := range result.ReferenceEvidence {
						if e.Status != "version_bound" || len(e.Uses) != 1 {
							t.Fatal("separate entries became ambiguous", e)
						}
						if e.Type == graph.EdgeTypeMountsVolume {
							continue
						}
						wantOptional := "required"
						if e.Type == graph.EdgeTypeUsesSecret {
							wantOptional = "optional"
						}
						use := e.Uses[0]
						if use.Mode != "environment" || use.Optional != wantOptional || use.Refresh != "container_restart_required" || !strings.Contains(use.FieldPath, "/"+field+"/0/"+mode+"/") {
							t.Fatal("separate environment reference lost declaration semantics", e)
						}
					}
					body, err := json.Marshal(result)
					if err != nil || strings.Contains(string(body), "canary") {
						t.Fatal("environment values or reference keys escaped", string(body), err)
					}
				})
			}
		}
	}
}
