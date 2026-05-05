package aggregator

import (
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestRenderMermaid_EmptyView(t *testing.T) {
	if got := renderMermaid(nil); got != "" {
		t.Errorf("nil view: got %q, want empty", got)
	}
	if got := renderMermaid(&View{}); got != "" {
		t.Errorf("zero-node view: got %q, want empty", got)
	}
}

func TestRenderMermaid_HappyPath(t *testing.T) {
	view := &View{
		Level: LevelResource,
		Nodes: []Node{
			{ID: "demo/Deployment/api", Type: "resource", Kind: "Deployment", Name: "api"},
			{ID: "demo/ConfigMap/cm", Type: "resource", Kind: "ConfigMap", Name: "cm"},
		},
		Edges: []AEdge{
			{From: "demo/Deployment/api", To: "demo/ConfigMap/cm", Type: graph.EdgeTypeUsesConfigMap, Count: 1},
		},
	}
	out := renderMermaid(view)
	if !strings.HasPrefix(out, "flowchart TD\n") {
		t.Errorf("output missing flowchart prefix: %q", out)
	}
	if !strings.Contains(out, `n0["Deployment/api"]`) {
		t.Errorf("output missing first node label: %q", out)
	}
	if !strings.Contains(out, `n1["ConfigMap/cm"]`) {
		t.Errorf("output missing second node label: %q", out)
	}
	if !strings.Contains(out, `n0 -->|USES_CONFIGMAP| n1`) {
		t.Errorf("output missing typed edge: %q", out)
	}
}

func TestRenderMermaid_OmitsAboveCap(t *testing.T) {
	view := &View{Level: LevelResource}
	for i := 0; i <= MaxResourceNeighbors; i++ {
		view.Nodes = append(view.Nodes, Node{ID: "x", Type: "resource"})
	}
	if got := renderMermaid(view); got != "" {
		t.Errorf("expected empty mermaid for >cap nodes, got %d-byte output", len(got))
	}
}

func TestRenderMermaid_EscapesQuotesAndPipes(t *testing.T) {
	view := &View{
		Level: LevelResource,
		Nodes: []Node{
			{ID: `weird/ConfigMap/has"quotes`, Type: "resource", Label: `has"quotes|here`},
		},
	}
	out := renderMermaid(view)
	if strings.Contains(out, `"`+`has"`) {
		t.Errorf("raw quote leaked into label: %q", out)
	}
	if strings.Contains(out, "|here") {
		t.Errorf("raw pipe leaked into label: %q", out)
	}
	if !strings.Contains(out, `#quot;`) {
		t.Errorf("expected #quot; escape in: %q", out)
	}
	if !strings.Contains(out, `#124;`) {
		t.Errorf("expected #124; escape in: %q", out)
	}
}

func TestRenderMermaid_FallsBackToLabelOrID(t *testing.T) {
	view := &View{
		Level: LevelResource,
		Nodes: []Node{
			{ID: "ns/Kind/name-set", Type: "resource", Kind: "Pod", Name: "lonely"},
			{ID: "fallback-id", Type: "resource", Label: "Just a Label"},
			{ID: "no-name-no-label", Type: "resource"},
		},
	}
	out := renderMermaid(view)
	if !strings.Contains(out, `"Pod/lonely"`) {
		t.Errorf("expected Kind/Name label for first node: %q", out)
	}
	if !strings.Contains(out, `"Just a Label"`) {
		t.Errorf("expected Label fallback for second node: %q", out)
	}
	if !strings.Contains(out, `"no-name-no-label"`) {
		t.Errorf("expected ID fallback for third node: %q", out)
	}
}
