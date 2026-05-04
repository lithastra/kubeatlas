package graph

import (
	"fmt"
	"strings"
)

// Distinct colors per resource kind for visual differentiation.
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

// ToDOT renders a Graph as a Graphviz DOT-format string.
func ToDOT(g *Graph) string {
	var b strings.Builder
	b.WriteString("digraph KubeAtlas {\n")
	b.WriteString("  rankdir=LR;\n")
	b.WriteString("  node [shape=box, style=filled, fontname=\"Helvetica\"];\n\n")

	// Nodes
	for _, r := range g.Resources {
		color := kindColors[r.Kind]
		if color == "" {
			color = "#CCCCCC"
		}
		// Label format: "Kind\nNamespace/Name" so two same-named resources in
		// different namespaces are visually distinct. Cluster-scoped resources
		// (empty Namespace) render as "Kind\n/Name", matching Resource.ID().
		label := fmt.Sprintf("%s\\n%s/%s", r.Kind, r.Namespace, r.Name)
		fmt.Fprintf(&b, "  \"%s\" [label=\"%s\", fillcolor=\"%s\", fontcolor=\"white\"];\n",
			r.ID(), label, color)
	}

	b.WriteString("\n")

	// Edges
	for _, e := range g.Edges {
		fmt.Fprintf(&b, "  \"%s\" -> \"%s\" [label=\"%s\"];\n",
			e.From, e.To, string(e.Type))
	}

	b.WriteString("}\n")
	return b.String()
}
