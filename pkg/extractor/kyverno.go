// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// kyvernoGroupPrefix gates the extractor to Kyverno policy objects.
const kyvernoGroupPrefix = "kyverno.io/"

// KyvernoExtractor emits ENFORCES edges from a Kyverno ClusterPolicy or
// Policy to the resources its validate rules select, overlaying the
// pass/fail/warn status from PolicyReports when they are available.
//
// Only validate rules are modelled — mutate and generate rules change
// or create resources rather than constrain them, so they are not an
// ENFORCES relationship. KubeAtlas reads the PolicyReport results the
// Kyverno controller writes; it never evaluates a policy. When the
// PolicyReport feature is off the edges still appear, just without a
// result attribute (degraded gracefully).
type KyvernoExtractor struct{}

// Type reports the edge type this extractor produces.
func (KyvernoExtractor) Type() graph.EdgeType { return graph.EdgeTypeEnforces }

// Extract returns one ENFORCES edge per resource the policy's validate
// rules select. Non-Kyverno-policy resources yield nothing.
func (KyvernoExtractor) Extract(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
	if !strings.HasPrefix(r.GroupVersion, kyvernoGroupPrefix) {
		return nil, nil
	}
	if r.Kind != "ClusterPolicy" && r.Kind != "Policy" {
		return nil, nil
	}

	rules := nestedSlice(r.Raw, "spec", "rules")
	if len(rules) == 0 {
		return nil, nil
	}

	reports := buildKyvernoReportIndex(ctx, q)
	from := r.ID()
	seen := make(map[string]struct{})
	var edges []graph.Edge

	for _, ruleAny := range rules {
		rule, _ := ruleAny.(map[string]any)
		if _, isValidate := rule["validate"]; !isValidate {
			// Only validate rules constrain resources; skip
			// mutate/generate.
			continue
		}
		for _, desc := range kyvernoMatchDescriptors(rule) {
			namespaces := desc.namespaces
			if r.Kind == "Policy" {
				// A namespaced Policy only ever applies in its own
				// namespace, whatever the descriptor says.
				namespaces = []string{r.Namespace}
			}
			if len(namespaces) == 0 {
				namespaces = []string{""}
			}
			for _, kind := range desc.kinds {
				for _, ns := range namespaces {
					matched, err := q.ListResources(ctx, graph.Filter{
						Kind:      kind,
						Namespace: ns,
						Labels:    desc.labels,
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
						if result, ok := reports[reportKey(r.Name, m.Kind, m.Namespace, m.Name)]; ok {
							e.Attributes = map[string]string{"result": result}
						}
						edges = append(edges, e)
					}
				}
			}
		}
	}
	return edges, nil
}

// kyvernoDescriptor is one resource selector from a rule's match block.
type kyvernoDescriptor struct {
	kinds      []string
	namespaces []string
	labels     map[string]string
}

// kyvernoMatchDescriptors flattens rule.match.any[] and rule.match.all[]
// into selectors. any/all differ in admission semantics, but for the
// graph's "which resources does this policy touch" view the union is
// the right over-approximation.
func kyvernoMatchDescriptors(rule map[string]any) []kyvernoDescriptor {
	var out []kyvernoDescriptor
	for _, group := range []string{"any", "all"} {
		for _, entryAny := range nestedSlice(rule, "match", group) {
			entry, _ := entryAny.(map[string]any)
			kinds := normalizeKinds(nestedSlice(entry, "resources", "kinds"))
			if len(kinds) == 0 {
				continue
			}
			out = append(out, kyvernoDescriptor{
				kinds:      kinds,
				namespaces: stringSlice(nestedSlice(entry, "resources", "namespaces")),
				labels:     nestedStringMap(entry, "resources", "selector", "matchLabels"),
			})
		}
	}
	return out
}

// normalizeKinds reduces Kyverno kind entries (which may be a bare Kind
// or a "group/version/Kind" triple) to the Kind KubeAtlas keys on.
func normalizeKinds(vals []any) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		if i := strings.LastIndex(s, "/"); i >= 0 {
			s = s[i+1:]
		}
		if s != "" && s != "*" {
			out = append(out, s)
		}
	}
	return out
}

func stringSlice(vals []any) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// buildKyvernoReportIndex reads every PolicyReport / ClusterPolicyReport
// in the store into a (policy, resource) -> result lookup. Missing
// reports yield an empty index — the edges then carry no result.
func buildKyvernoReportIndex(ctx context.Context, q graph.ResourceLister) map[string]string {
	idx := make(map[string]string)
	for _, kind := range []string{"PolicyReport", "ClusterPolicyReport"} {
		reports, err := q.ListResources(ctx, graph.Filter{Kind: kind})
		if err != nil {
			continue
		}
		for _, rep := range reports {
			for _, resAny := range nestedSlice(rep.Raw, "results") {
				res, _ := resAny.(map[string]any)
				policy := asString(res["policy"])
				result := asString(res["result"])
				if policy == "" || result == "" {
					continue
				}
				for _, refAny := range nestedSlice(res, "resources") {
					ref, _ := refAny.(map[string]any)
					idx[reportKey(policy, asString(ref["kind"]), asString(ref["namespace"]), asString(ref["name"]))] = result
				}
			}
		}
	}
	return idx
}

func reportKey(policy, kind, namespace, name string) string {
	return policy + "|" + kind + "/" + namespace + "/" + name
}
