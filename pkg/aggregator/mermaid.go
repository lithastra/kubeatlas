package aggregator

import (
	"strconv"
	"strings"
)

// renderMermaid converts a (resource-level) View into a Mermaid
// flowchart text. The output goes straight into mermaid.render on the
// client; doing it server-side ensures every consumer (Web UI, CLI,
// future Headlamp plugin) sees the same edge-direction + label
// conventions, and keeps node-id escaping in one place where the
// edge-extraction rules already live.
//
// Spec contract (Phase 1 §2.6):
//   - Server generates Mermaid text for resource-level views.
//   - Skipped above MaxResourceNeighbors (Mermaid struggles >50 nodes;
//     the renderer in the UI already steers users to Cytoscape).
//   - Direction is top-down (TD) so chained owner edges read
//     naturally (Pod -> ReplicaSet -> Deployment as a column).
func renderMermaid(view *View) string {
	if view == nil || len(view.Nodes) == 0 {
		return ""
	}
	if len(view.Nodes) > MaxResourceNeighbors {
		// Caller is expected to flip Truncated; suppress Mermaid so
		// the client doesn't try to render an oversized graph.
		return ""
	}

	// Map each id to a short, mermaid-safe handle. Mermaid node ids
	// must match [A-Za-z0-9_]+; resource ids contain '/' which is
	// disallowed.
	idHandle := make(map[string]string, len(view.Nodes))
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	for i, n := range view.Nodes {
		h := "n" + strconv.Itoa(i)
		idHandle[n.ID] = h
		b.WriteString("    ")
		b.WriteString(h)
		b.WriteString(`["`)
		b.WriteString(escapeMermaidLabel(nodeMermaidLabel(n)))
		b.WriteString(`"]`)
		b.WriteByte('\n')
	}
	for _, e := range view.Edges {
		from, fok := idHandle[e.From]
		to, tok := idHandle[e.To]
		if !fok || !tok {
			// Either endpoint dropped during truncation; skip the edge
			// rather than emit a dangling reference.
			continue
		}
		b.WriteString("    ")
		b.WriteString(from)
		b.WriteString(" -->|")
		b.WriteString(escapeMermaidLabel(string(e.Type)))
		b.WriteString("| ")
		b.WriteString(to)
		b.WriteByte('\n')
	}
	return b.String()
}

// nodeMermaidLabel picks the most useful display label for a node:
// "Kind/Name" if both are set (the common case for resource-level
// nodes), else falls back to label or id.
func nodeMermaidLabel(n Node) string {
	if n.Kind != "" && n.Name != "" {
		return n.Kind + "/" + n.Name
	}
	if n.Label != "" {
		return n.Label
	}
	return n.ID
}

// escapeMermaidLabel makes a string safe for use inside Mermaid's
// double-quoted label syntax. Mermaid uses #quot;-style HTML
// references for problematic chars; we rewrite the small set we
// actually emit (quotes, brackets, pipes) and pass everything else
// through.
func escapeMermaidLabel(s string) string {
	r := strings.NewReplacer(
		`"`, `#quot;`,
		`<`, `#lt;`,
		`>`, `#gt;`,
		`|`, `#124;`,
		"\n", " ",
	)
	return r.Replace(s)
}
