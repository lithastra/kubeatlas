// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// gatekeeperConstraintGroupPrefix identifies a Gatekeeper Constraint by
// its API group. Constraints generated from a ConstraintTemplate live
// under constraints.gatekeeper.sh/<version>; the extractor gates on
// this prefix so it ignores every other resource cheaply.
const gatekeeperConstraintGroupPrefix = "constraints.gatekeeper.sh/"

// GatekeeperExtractor emits ENFORCES edges from a Gatekeeper Constraint
// to every resource its spec.match selects, tagging the edge with
// violation status read from the Constraint's status.violations.
//
// KubeAtlas does not evaluate the Constraint's Rego — it reads the
// status the Gatekeeper controller already computed (a read-only
// observation, not an admission decision). The Constraint kinds appear
// at runtime (one per ConstraintTemplate), so the resource feeding this
// extractor arrives through the dynamic-informer path; the extraction
// itself is a pure function of the Constraint plus the store.
type GatekeeperExtractor struct{}

// Type reports the edge type this extractor produces.
func (GatekeeperExtractor) Type() graph.EdgeType { return graph.EdgeTypeEnforces }

// Extract returns one ENFORCES edge per resource the Constraint
// matches. Non-Constraint resources yield nothing.
func (GatekeeperExtractor) Extract(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
	if !strings.HasPrefix(r.GroupVersion, gatekeeperConstraintGroupPrefix) {
		return nil, nil
	}

	kinds := constraintMatchKinds(r)
	if len(kinds) == 0 {
		return nil, nil
	}
	namespaces := constraintMatchNamespaces(r)
	labels := nestedStringMap(r.Raw, "spec", "match", "labelSelector", "matchLabels")
	violations := constraintViolations(r)

	// An empty namespace list means "every namespace" — the store's
	// Filter treats an empty Namespace as no namespace constraint.
	nsScopes := namespaces
	if len(nsScopes) == 0 {
		nsScopes = []string{""}
	}

	from := r.ID()
	seen := make(map[string]struct{})
	var edges []graph.Edge

	for _, kind := range kinds {
		for _, ns := range nsScopes {
			matched, err := q.ListResources(ctx, graph.Filter{
				Kind:      kind,
				Namespace: ns,
				Labels:    labels,
				ClusterID: r.ClusterID,
			})
			if err != nil {
				return nil, err
			}
			for _, m := range matched {
				to := m.ID()
				if _, dup := seen[to]; dup {
					continue
				}
				seen[to] = struct{}{}

				e := graph.Edge{From: from, To: to, Type: graph.EdgeTypeEnforces}
				if msg, violated := violations[violationKey(m.Kind, m.Namespace, m.Name)]; violated {
					e.Attributes = map[string]string{
						"violated":          "true",
						"violation_message": msg,
					}
				}
				edges = append(edges, e)
			}
		}
	}
	return edges, nil
}

// constraintMatchKinds flattens spec.match.kinds[].kinds into a single
// list of Kind strings. The apiGroups sibling is intentionally not used
// for matching: KubeAtlas keys resources by Kind, and a Constraint that
// reuses a Kind name across groups is vanishingly rare.
func constraintMatchKinds(r graph.Resource) []string {
	var out []string
	for _, entry := range nestedSlice(r.Raw, "spec", "match", "kinds") {
		em, _ := entry.(map[string]any)
		for _, k := range nestedSlice(em, "kinds") {
			if s, ok := k.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// constraintMatchNamespaces reads spec.match.namespaces.
func constraintMatchNamespaces(r graph.Resource) []string {
	var out []string
	for _, n := range nestedSlice(r.Raw, "spec", "match", "namespaces") {
		if s, ok := n.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// constraintViolations indexes status.violations by kind/namespace/name
// so the extractor can tag the matching ENFORCES edge.
func constraintViolations(r graph.Resource) map[string]string {
	out := make(map[string]string)
	for _, v := range nestedSlice(r.Raw, "status", "violations") {
		vm, _ := v.(map[string]any)
		key := violationKey(asString(vm["kind"]), asString(vm["namespace"]), asString(vm["name"]))
		out[key] = asString(vm["message"])
	}
	return out
}

func violationKey(kind, namespace, name string) string {
	return kind + "/" + namespace + "/" + name
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
