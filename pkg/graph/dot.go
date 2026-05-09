package graph

import (
	"fmt"
	"sort"
	"strings"
)

// kindColors picks a fill color per resource Kind. The palette is
// the one PoC validation locked in — kept stable so DOT output
// stays diff-friendly across versions.
var kindColors = map[string]string{
	"Deployment":  "#4A90D9",
	"ReplicaSet":  "#6FA8DC",
	"Pod":         "#9FC5E8",
	"Service":     "#7B68EE",
	"ConfigMap":   "#50C878",
	"Secret":      "#FF6347",
	"Ingress":     "#FFA500",
	"Gateway":     "#E67E22",
	"HTTPRoute":   "#F4A261",
	"StatefulSet": "#4A90D9",
	"DaemonSet":   "#4A90D9",
}

// edgeColors picks a stroke color per EdgeType. Operationally
// related types share a hue: ownership in greys, mounts / consumes
// in greens, routing in oranges, RBAC in purples.
var edgeColors = map[EdgeType]string{
	EdgeTypeOwns:               "#555555",
	EdgeTypeUsesConfigMap:      "#2E8B57",
	EdgeTypeUsesSecret:         "#B22222",
	EdgeTypeMountsVolume:       "#3CB371",
	EdgeTypeSelects:            "#1E90FF",
	EdgeTypeUsesServiceAccount: "#9370DB",
	EdgeTypeRoutesTo:           "#E67E22",
	EdgeTypeAttachedTo:         "#FF8C00",
	EdgeTypeBindsSubject:       "#8A2BE2",
	EdgeTypeBindsRole:          "#9932CC",
}

// DOTOptions narrows what ToDOT renders. Empty Namespace = whole
// graph. Title overrides the default "KubeAtlas" digraph name when
// set — useful for distinguishing multiple SVG outputs in a single
// dashboard.
type DOTOptions struct {
	Namespace string
	Title     string
}

// ToDOT renders a Graph as a Graphviz DOT-format string with
// default options (whole graph, default title). Kept for
// backward-compatible call sites; new callers should use
// ToDOTOptions when they need filtering.
func ToDOT(g *Graph) string {
	return ToDOTOptions(g, DOTOptions{})
}

// ToDOTOptions renders a Graph as a DOT string honouring opts.
// Resources are emitted in stable name order so successive runs
// produce byte-identical output (anti-pattern: don't make output
// non-deterministic).
func ToDOTOptions(g *Graph, opts DOTOptions) string {
	if g == nil {
		return ""
	}
	title := opts.Title
	if title == "" {
		title = "KubeAtlas"
	}

	included := make(map[string]bool, len(g.Resources))
	resources := make([]Resource, 0, len(g.Resources))
	for _, r := range g.Resources {
		if opts.Namespace != "" && r.Namespace != opts.Namespace {
			continue
		}
		included[r.ID()] = true
		resources = append(resources, r)
	}
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].ID() < resources[j].ID()
	})

	edges := make([]Edge, 0, len(g.Edges))
	for _, e := range g.Edges {
		if !included[e.From] || !included[e.To] {
			continue
		}
		edges = append(edges, e)
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return string(edges[i].Type) < string(edges[j].Type)
	})

	var b strings.Builder
	fmt.Fprintf(&b, "digraph %s {\n", dotIdent(title))
	b.WriteString("  rankdir=LR;\n")
	b.WriteString("  node [shape=box, style=filled, fontname=\"Helvetica\"];\n\n")

	for _, r := range resources {
		color := kindColors[r.Kind]
		if color == "" {
			color = "#CCCCCC"
		}
		// Label format: "Kind\nNamespace/Name" so two same-named
		// resources in different namespaces are visually distinct.
		// Cluster-scoped resources (empty Namespace) render as
		// "Kind\n/Name", matching Resource.ID().
		label := fmt.Sprintf("%s\\n%s/%s", r.Kind, r.Namespace, r.Name)
		fmt.Fprintf(&b, "  \"%s\" [label=\"%s\", fillcolor=\"%s\", fontcolor=\"white\"];\n",
			r.ID(), label, color)
	}

	if len(edges) > 0 {
		b.WriteString("\n")
		for _, e := range edges {
			edgeColor := edgeColors[e.Type]
			if edgeColor == "" {
				edgeColor = "#999999"
			}
			fmt.Fprintf(&b, "  \"%s\" -> \"%s\" [label=\"%s\", color=\"%s\"];\n",
				e.From, e.To, string(e.Type), edgeColor)
		}
	}

	b.WriteString("}\n")
	return b.String()
}

// dotIdent normalises a string into something safe to drop after
// the `digraph` keyword without quoting. Anything outside
// [A-Za-z0-9_] becomes `_`. Kept private since callers should pass
// human-readable Title values and let ToDOTOptions do the
// sanitising.
func dotIdent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z',
			r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "graph"
	}
	return b.String()
}
