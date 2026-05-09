package graph

import (
	"strings"
	"testing"
)

func TestToDOT_ContainsHeaderNodesAndEdge(t *testing.T) {
	g := &Graph{
		Resources: []Resource{
			{Kind: "Deployment", Name: "web-app", Namespace: "demo"},
			{Kind: "ConfigMap", Name: "app-config", Namespace: "demo"},
		},
		Edges: []Edge{
			{
				From: "demo/Deployment/web-app",
				To:   "demo/ConfigMap/app-config",
				Type: EdgeTypeUsesConfigMap,
			},
		},
	}

	out := ToDOT(g)

	wants := []string{
		"digraph KubeAtlas",
		"rankdir=LR",
		"\"demo/Deployment/web-app\"",
		"\"demo/ConfigMap/app-config\"",
		"label=\"Deployment\\ndemo/web-app\"",
		"label=\"ConfigMap\\ndemo/app-config\"",
		"\"demo/Deployment/web-app\" -> \"demo/ConfigMap/app-config\"",
		"label=\"USES_CONFIGMAP\"",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("ToDOT output missing %q\n--- output ---\n%s", w, out)
		}
	}
}

func TestToDOTOptions_NamespaceFilter(t *testing.T) {
	g := &Graph{
		Resources: []Resource{
			{Kind: "Deployment", Name: "web", Namespace: "demo"},
			{Kind: "Deployment", Name: "api", Namespace: "other"},
			{Kind: "ConfigMap", Name: "cm", Namespace: "demo"},
		},
		Edges: []Edge{
			{From: "demo/Deployment/web", To: "demo/ConfigMap/cm", Type: EdgeTypeUsesConfigMap},
			{From: "other/Deployment/api", To: "demo/ConfigMap/cm", Type: EdgeTypeUsesConfigMap},
		},
	}

	out := ToDOTOptions(g, DOTOptions{Namespace: "demo"})

	if !strings.Contains(out, "demo/Deployment/web") {
		t.Errorf("expected demo/Deployment/web in output:\n%s", out)
	}
	if strings.Contains(out, "other/Deployment/api") {
		t.Errorf("namespace filter let other-ns resource through:\n%s", out)
	}
	// The cross-namespace edge must be dropped because one endpoint
	// is filtered out.
	if strings.Contains(out, "other/Deployment/api\" -> \"demo/ConfigMap/cm") {
		t.Errorf("cross-namespace edge survived filter:\n%s", out)
	}
}

func TestToDOTOptions_TitleSanitised(t *testing.T) {
	out := ToDOTOptions(&Graph{}, DOTOptions{Title: "kube-atlas demo!"})
	if !strings.Contains(out, "digraph kube_atlas_demo_") {
		t.Errorf("title not sanitised:\n%s", out)
	}
}

func TestToDOTOptions_DeterministicOrder(t *testing.T) {
	// Two runs over the same graph must produce byte-identical
	// output — the playbook explicitly calls this out (PoC parity:
	// "byte-identical or near-identical, allow attribute reordering"
	// — we can do better and produce identical output).
	g := &Graph{
		Resources: []Resource{
			{Kind: "ConfigMap", Name: "z", Namespace: "demo"},
			{Kind: "ConfigMap", Name: "a", Namespace: "demo"},
			{Kind: "ConfigMap", Name: "m", Namespace: "demo"},
		},
		Edges: []Edge{
			{From: "demo/ConfigMap/z", To: "demo/ConfigMap/a", Type: EdgeTypeUsesConfigMap},
			{From: "demo/ConfigMap/a", To: "demo/ConfigMap/m", Type: EdgeTypeUsesConfigMap},
		},
	}
	first := ToDOT(g)
	for i := 0; i < 5; i++ {
		if got := ToDOT(g); got != first {
			t.Fatalf("ToDOT non-deterministic on iteration %d", i)
		}
	}
}

func TestToDOT_NilGraph(t *testing.T) {
	if got := ToDOT(nil); got != "" {
		t.Errorf("ToDOT(nil) = %q, want empty", got)
	}
}

func TestToDOT_EdgeColorByType(t *testing.T) {
	g := &Graph{
		Resources: []Resource{
			{Kind: "ServiceAccount", Name: "sa", Namespace: "demo"},
			{Kind: "RoleBinding", Name: "rb", Namespace: "demo"},
		},
		Edges: []Edge{
			{From: "demo/RoleBinding/rb", To: "demo/ServiceAccount/sa", Type: EdgeTypeBindsSubject},
		},
	}
	out := ToDOT(g)
	if !strings.Contains(out, "color=\"#8A2BE2\"") {
		t.Errorf("BINDS_SUBJECT edge missing its color:\n%s", out)
	}
}
