package extractor

import (
	"sort"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// rbHelper builds a graph.Resource that looks like an unstructured
// RoleBinding the informer would emit. Tests only set the fields the
// extractor reads (kind / name / namespace / subjects / roleRef).
func rbHelper(ns, name string, subjects []map[string]any, roleRef map[string]any) graph.Resource {
	return graph.Resource{
		Kind:      "RoleBinding",
		Namespace: ns,
		Name:      name,
		Raw: map[string]any{
			"subjects": toAnySlice(subjects),
			"roleRef":  roleRef,
		},
	}
}

// crbHelper is the cluster-scoped twin.
func crbHelper(name string, subjects []map[string]any, roleRef map[string]any) graph.Resource {
	return graph.Resource{
		Kind: "ClusterRoleBinding",
		Name: name,
		Raw: map[string]any{
			"subjects": toAnySlice(subjects),
			"roleRef":  roleRef,
		},
	}
}

func toAnySlice(in []map[string]any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

func TestBindsSubjectExtractor_RoleBindingDefaultsNamespace(t *testing.T) {
	rb := rbHelper("demo", "api-can-list-cm",
		[]map[string]any{
			// SA without explicit namespace defaults to RB's ns.
			{"kind": "ServiceAccount", "name": "api-sa"},
			// User and Group are namespace-less.
			{"kind": "User", "name": "alice"},
			{"kind": "Group", "name": "dev-team"},
		},
		map[string]any{"kind": "Role", "name": "list-cm"},
	)

	got := extractEdges(t, BindsSubjectExtractor{}, rb, nil)
	wantTo := []string{
		"demo/ServiceAccount/api-sa",
		"/User/alice",
		"/Group/dev-team",
	}
	if len(got) != len(wantTo) {
		t.Fatalf("edges = %d, want %d (%+v)", len(got), len(wantTo), got)
	}
	for i, w := range wantTo {
		if got[i].To != w || got[i].Type != graph.EdgeTypeBindsSubject || got[i].From != "demo/RoleBinding/api-can-list-cm" {
			t.Errorf("edge[%d] = %+v, want To=%s", i, got[i], w)
		}
	}
}

func TestBindsSubjectExtractor_ClusterRoleBindingNoNamespaceDefault(t *testing.T) {
	// In a CRB, SA subjects MUST carry their own namespace; we do
	// not back-fill from the binding (which is cluster-scoped and
	// has no namespace of its own).
	crb := crbHelper("cluster-admins",
		[]map[string]any{
			{"kind": "ServiceAccount", "name": "deployer", "namespace": "kube-system"},
		},
		map[string]any{"kind": "ClusterRole", "name": "cluster-admin"},
	)
	got := extractEdges(t, BindsSubjectExtractor{}, crb, nil)
	if len(got) != 1 {
		t.Fatalf("edges = %d, want 1", len(got))
	}
	if got[0].To != "kube-system/ServiceAccount/deployer" {
		t.Errorf("edge.To = %q, want kube-system/ServiceAccount/deployer", got[0].To)
	}
}

func TestBindsSubjectExtractor_SkipsMalformedSubjects(t *testing.T) {
	rb := rbHelper("demo", "broken",
		[]map[string]any{
			{"kind": "", "name": "no-kind"},
			{"kind": "User"}, // no name
			{"name": "no-kind"},
			{"kind": "User", "name": "alice"}, // good
		},
		map[string]any{"kind": "Role", "name": "x"},
	)
	got := extractEdges(t, BindsSubjectExtractor{}, rb, nil)
	if len(got) != 1 || got[0].To != "/User/alice" {
		t.Errorf("edges = %+v, want one edge to /User/alice", got)
	}
}

func TestBindsSubjectExtractor_KindMismatchSkipped(t *testing.T) {
	notRB := graph.Resource{Kind: "Pod", Name: "x"}
	got := extractEdges(t, BindsSubjectExtractor{}, notRB, nil)
	if len(got) != 0 {
		t.Errorf("non-RB resource produced %d edges, want 0", len(got))
	}
}

func TestBindsRoleExtractor_RoleInBindingNamespace(t *testing.T) {
	rb := rbHelper("demo", "binding",
		nil,
		map[string]any{"kind": "Role", "name": "list-cm"},
	)
	got := extractEdges(t, BindsRoleExtractor{}, rb, nil)
	if len(got) != 1 {
		t.Fatalf("edges = %d, want 1", len(got))
	}
	if got[0].To != "demo/Role/list-cm" || got[0].Type != graph.EdgeTypeBindsRole {
		t.Errorf("edge = %+v, want demo/Role/list-cm BINDS_ROLE", got[0])
	}
}

func TestBindsRoleExtractor_RoleBindingReferencingClusterRole(t *testing.T) {
	// RoleBindings can grant a ClusterRole within their own ns;
	// the edge target is cluster-scoped though.
	rb := rbHelper("demo", "rb-uses-cr",
		nil,
		map[string]any{"kind": "ClusterRole", "name": "view"},
	)
	got := extractEdges(t, BindsRoleExtractor{}, rb, nil)
	if len(got) != 1 || got[0].To != "/ClusterRole/view" {
		t.Errorf("edge = %+v, want /ClusterRole/view", got)
	}
}

func TestBindsRoleExtractor_ClusterRoleBinding(t *testing.T) {
	crb := crbHelper("admins", nil,
		map[string]any{"kind": "ClusterRole", "name": "cluster-admin"},
	)
	got := extractEdges(t, BindsRoleExtractor{}, crb, nil)
	if len(got) != 1 || got[0].To != "/ClusterRole/cluster-admin" {
		t.Errorf("edge = %+v, want /ClusterRole/cluster-admin", got)
	}
	if got[0].From != "/ClusterRoleBinding/admins" {
		t.Errorf("edge.From = %q, want /ClusterRoleBinding/admins", got[0].From)
	}
}

func TestBindsRoleExtractor_MalformedRoleRefSkipped(t *testing.T) {
	cases := []struct {
		name    string
		roleRef map[string]any
	}{
		{"missing kind", map[string]any{"name": "x"}},
		{"missing name", map[string]any{"kind": "Role"}},
		{"empty kind", map[string]any{"kind": "", "name": "x"}},
		{"empty name", map[string]any{"kind": "Role", "name": ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rb := rbHelper("demo", "x", nil, c.roleRef)
			got := extractEdges(t, BindsRoleExtractor{}, rb, nil)
			if len(got) != 0 {
				t.Errorf("malformed roleRef %v produced %d edges, want 0", c.roleRef, len(got))
			}
		})
	}
}

func TestBindsRoleExtractor_NoRoleRefSkipped(t *testing.T) {
	rb := graph.Resource{
		Kind: "RoleBinding", Namespace: "demo", Name: "broken",
		Raw: map[string]any{"subjects": []any{}},
	}
	got := extractEdges(t, BindsRoleExtractor{}, rb, nil)
	if len(got) != 0 {
		t.Errorf("missing roleRef produced %d edges, want 0", len(got))
	}
}

// TestDefault_RegistersRBACExtractors is a tripwire so the registry
// stays in sync with graph.AllEdgeTypes — adding an edge type
// without registering its extractor here flips this test red.
func TestDefault_RegistersRBACExtractors(t *testing.T) {
	reg := Default()
	rb := rbHelper("demo", "binding",
		[]map[string]any{{"kind": "ServiceAccount", "name": "default"}},
		map[string]any{"kind": "Role", "name": "list-cm"},
	)
	edges := extractAllEdges(t, reg, rb, nil)
	types := make([]string, 0, len(edges))
	for _, e := range edges {
		types = append(types, string(e.Type))
	}
	sort.Strings(types)
	want := []string{"BINDS_ROLE", "BINDS_SUBJECT"}
	if len(types) != 2 || types[0] != want[0] || types[1] != want[1] {
		t.Errorf("registered RBAC edges = %v, want %v", types, want)
	}
}
