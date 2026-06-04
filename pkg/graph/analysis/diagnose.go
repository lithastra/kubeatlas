// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis

import (
	"context"
	"sort"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// maxDiagnoseBlastDepth caps the per-resource blast-radius walk the
// diagnose report runs. The store clamps traversal at 10 hops; we ask
// for the clamp so the "top blast radius" ranking reflects the deepest
// impact the store will compute.
const maxDiagnoseBlastDepth = 10

// topBlastRadiusN is how many resources the report ranks by blast
// radius. F-301 surfaces the TOP-N most impactful resources.
const topBlastRadiusN = 10

// DiagnoseScope selects what the report covers: a single namespace, or
// the whole cluster (AllNamespaces). The zero value (empty Namespace,
// AllNamespaces=false) is treated as whole-cluster so a bare call does
// the most useful thing.
type DiagnoseScope struct {
	Namespace     string `json:"namespace,omitempty"`
	AllNamespaces bool   `json:"allNamespaces"`
}

// clusterScoped reports whether the scope covers the whole cluster.
func (s DiagnoseScope) clusterScoped() bool {
	return s.AllNamespaces || s.Namespace == ""
}

// namespaceFilter returns the namespace to filter on, or "" for a
// whole-cluster sweep.
func (s DiagnoseScope) namespaceFilter() string {
	if s.clusterScoped() {
		return ""
	}
	return s.Namespace
}

// String renders the scope for the report header.
func (s DiagnoseScope) String() string {
	if s.clusterScoped() {
		return "all namespaces"
	}
	return "namespace " + s.Namespace
}

// BlastRadiusEntry pairs a resource with the number of resources that
// transitively depend on it (its incoming blast radius). Used by the
// report's "top blast radius" ranking.
type BlastRadiusEntry struct {
	Resource graph.Resource `json:"resource"`
	Affected int            `json:"affected"`
}

// PolicyViolation is one resource a policy engine is currently failing:
// a single ENFORCES edge whose target is in violation. It normalises
// the per-engine attribute schemas (Gatekeeper tags edges with
// violated=true + violation_message; Kyverno tags them with
// result=fail) into one engine-agnostic shape so automation — the
// GitHub Action's PR comment, dashboards — has a stable contract
// instead of reaching into raw edge attributes.
type PolicyViolation struct {
	Policy   string `json:"policy"`            // enforcing resource id (the Constraint / ClusterPolicy)
	Resource string `json:"resource"`          // the resource in violation
	Message  string `json:"message,omitempty"` // engine-supplied reason, when available
}

// DiagnoseReport is the self-contained F-301 diagnostic snapshot: the
// scoped dependency graph plus orphan, cycle, and blast-radius
// analysis. It serialises to JSON for automation and renders to a
// standalone HTML report (see RenderHTML) for humans / air-gapped
// audits.
type DiagnoseReport struct {
	GeneratedAt      time.Time          `json:"generatedAt"`
	KubeAtlasVersion string             `json:"kubeAtlasVersion"`
	Scope            DiagnoseScope      `json:"scope"`
	Graph            *graph.Graph       `json:"graph"`
	Orphans          []OrphanReport     `json:"orphans"`
	Cycles           []CycleReport      `json:"cycles"`
	TopBlastRadius   []BlastRadiusEntry `json:"topBlastRadius"`
	PolicyViolations []PolicyViolation  `json:"policyViolations"`
	ResourceCount    int                `json:"resourceCount"`
	EdgeCount        int                `json:"edgeCount"`
}

// GenerateReport assembles a DiagnoseReport for the given scope by
// composing the existing graph analysers (orphans, cycles, blast
// radius) over a single store. It renders nothing — callers pick JSON
// (marshal the struct) or HTML (RenderHTML).
//
// The graph is scoped: a namespace scope returns that namespace's
// subgraph; a cluster scope returns the full snapshot. Cycles are
// detected cluster-wide and, for a namespace scope, filtered to those
// touching the namespace — an unrelated cycle in another namespace is
// noise on a namespace report.
func GenerateReport(ctx context.Context, store graph.GraphStore, scope DiagnoseScope, kubeatlasVersion string) (*DiagnoseReport, error) {
	var (
		g   *graph.Graph
		err error
	)
	if scope.clusterScoped() {
		g, err = store.Snapshot(ctx)
	} else {
		g, err = store.NamespaceSubgraph(ctx, scope.Namespace, nil)
	}
	if err != nil {
		return nil, err
	}

	orphans, err := DetectOrphans(ctx, store, OrphanOptions{Namespace: scope.namespaceFilter()})
	if err != nil {
		return nil, err
	}

	cycles, err := DetectCycles(ctx, store)
	if err != nil {
		return nil, err
	}
	if !scope.clusterScoped() {
		cycles = filterCyclesToNamespace(cycles, scope.Namespace)
	}

	top, err := topBlastRadius(ctx, store, g.Resources)
	if err != nil {
		return nil, err
	}

	return &DiagnoseReport{
		GeneratedAt:      time.Now().UTC(),
		KubeAtlasVersion: kubeatlasVersion,
		Scope:            scope,
		Graph:            g,
		Orphans:          orphans,
		Cycles:           cycles,
		TopBlastRadius:   top,
		PolicyViolations: detectPolicyViolations(g.Edges),
		ResourceCount:    len(g.Resources),
		EdgeCount:        len(g.Edges),
	}, nil
}

// detectPolicyViolations distils the report's ENFORCES edges down to the
// ones currently in violation, normalising the two engines' attribute
// schemas:
//
//   - Gatekeeper edges carry violated="true" and violation_message=<reason>.
//   - Kyverno edges carry result="fail" (a pass leaves no violation).
//
// Edges without violation markers (compliant resources, or edges from an
// engine that does not report status) are skipped. The result is sorted
// by policy then resource so successive runs are byte-identical.
func detectPolicyViolations(edges []graph.Edge) []PolicyViolation {
	out := make([]PolicyViolation, 0)
	for _, e := range edges {
		if e.Type != graph.EdgeTypeEnforces || e.Attributes == nil {
			continue
		}
		violated := e.Attributes["violated"] == "true" || e.Attributes["result"] == "fail"
		if !violated {
			continue
		}
		out = append(out, PolicyViolation{
			Policy:   e.From,
			Resource: e.To,
			Message:  e.Attributes["violation_message"],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Policy != out[j].Policy {
			return out[i].Policy < out[j].Policy
		}
		return out[i].Resource < out[j].Resource
	})
	return out
}

// filterCyclesToNamespace keeps only cycles with at least one member in
// ns. One member is enough: a cycle that crosses into the namespace is
// relevant to it even if other members live elsewhere.
func filterCyclesToNamespace(cycles []CycleReport, ns string) []CycleReport {
	out := make([]CycleReport, 0, len(cycles))
	for _, c := range cycles {
		for _, m := range c.Members {
			if m.Namespace == ns {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// topBlastRadius ranks resources by how many other resources
// transitively depend on them (incoming blast radius), returning the
// top N with a non-zero radius in descending order. Ties break by
// resource id so successive runs are byte-identical.
func topBlastRadius(ctx context.Context, store graph.GraphStore, resources []graph.Resource) ([]BlastRadiusEntry, error) {
	entries := make([]BlastRadiusEntry, 0, len(resources))
	for _, r := range resources {
		affected, err := BlastRadius(ctx, store, r.ID(), Options{MaxDepth: maxDiagnoseBlastDepth})
		if err != nil {
			return nil, err
		}
		if len(affected) == 0 {
			continue
		}
		entries = append(entries, BlastRadiusEntry{Resource: r, Affected: len(affected)})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Affected != entries[j].Affected {
			return entries[i].Affected > entries[j].Affected
		}
		return entries[i].Resource.ID() < entries[j].Resource.ID()
	})
	if len(entries) > topBlastRadiusN {
		entries = entries[:topBlastRadiusN]
	}
	return entries, nil
}
