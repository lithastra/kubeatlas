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
