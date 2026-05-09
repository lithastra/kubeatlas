package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// seedRBACFixture stages a minimal but realistic RBAC topology in
// the store for the rbac handler tests:
//
//	demo/ServiceAccount/api-sa    — subject of api-can-list-cm
//	demo/RoleBinding/api-can-list-cm
//	    -> BINDS_SUBJECT   demo/ServiceAccount/api-sa
//	    -> BINDS_ROLE      demo/Role/list-cm
//	demo/Role/list-cm  (with Raw.rules describing get/list on configmaps)
//	/ClusterRoleBinding/cluster-admins
//	    -> BINDS_SUBJECT   ops/ServiceAccount/deployer
//	    -> BINDS_ROLE      /ClusterRole/cluster-admin
//	/ClusterRole/cluster-admin (with Raw.rules describing * on *)
func seedRBACFixture(t *testing.T) (string, func()) {
	t.Helper()
	addr, _, cleanup := seedAndServe(t, func(s graph.GraphStore) {
		ctx := context.Background()

		listCM := graph.Resource{
			Kind: "Role", Namespace: "demo", Name: "list-cm",
			Raw: map[string]any{"rules": []any{
				map[string]any{
					"apiGroups": []any{""},
					"resources": []any{"configmaps"},
					"verbs":     []any{"get", "list"},
				},
			}},
		}
		clusterAdmin := graph.Resource{
			Kind: "ClusterRole", Namespace: "", Name: "cluster-admin",
			Raw: map[string]any{"rules": []any{
				map[string]any{
					"apiGroups": []any{"*"},
					"resources": []any{"*"},
					"verbs":     []any{"*"},
				},
			}},
		}
		apiSA := graph.Resource{Kind: "ServiceAccount", Namespace: "demo", Name: "api-sa"}
		deployerSA := graph.Resource{Kind: "ServiceAccount", Namespace: "ops", Name: "deployer"}
		rb := graph.Resource{Kind: "RoleBinding", Namespace: "demo", Name: "api-can-list-cm"}
		crb := graph.Resource{Kind: "ClusterRoleBinding", Namespace: "", Name: "cluster-admins"}

		for _, r := range []graph.Resource{listCM, clusterAdmin, apiSA, deployerSA, rb, crb} {
			if err := s.UpsertResource(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		// Edges produced by the BindsSubject / BindsRole extractors.
		mustEdge(ctx, s, t, rb.ID(), apiSA.ID(), graph.EdgeTypeBindsSubject)
		mustEdge(ctx, s, t, rb.ID(), listCM.ID(), graph.EdgeTypeBindsRole)
		mustEdge(ctx, s, t, crb.ID(), deployerSA.ID(), graph.EdgeTypeBindsSubject)
		mustEdge(ctx, s, t, crb.ID(), clusterAdmin.ID(), graph.EdgeTypeBindsRole)
	})
	return addr, cleanup
}

func mustEdge(ctx context.Context, s graph.GraphStore, t *testing.T, from, to string, typ graph.EdgeType) {
	t.Helper()
	if err := s.UpsertEdge(ctx, graph.Edge{From: from, To: to, Type: typ}); err != nil {
		t.Fatal(err)
	}
}

// TestRBAC_ServiceAccountPermissions verifies the SA -> RB -> Role
// walk surfaces the bound role's rules.
func TestRBAC_ServiceAccountPermissions(t *testing.T) {
	addr, cleanup := seedRBACFixture(t)
	defer cleanup()

	resp, body := getJSON(t,
		addr+"/api/v1alpha1/rbac/serviceaccount/demo/api-sa/permissions", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	var got struct {
		Subject struct {
			Kind, Namespace, Name string
		}
		Bindings []struct {
			Binding struct {
				Kind, Namespace, Name string
			}
			Role struct {
				Kind, Namespace, Name string
			}
			Rules []struct {
				APIGroups, Resources, Verbs []string
			}
		}
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v\nbody=%s", err, body)
	}
	if got.Subject.Kind != "ServiceAccount" || got.Subject.Namespace != "demo" || got.Subject.Name != "api-sa" {
		t.Errorf("subject = %+v, want demo/api-sa", got.Subject)
	}
	if len(got.Bindings) != 1 {
		t.Fatalf("bindings = %d, want 1", len(got.Bindings))
	}
	b := got.Bindings[0]
	if b.Binding.Kind != "RoleBinding" || b.Binding.Name != "api-can-list-cm" {
		t.Errorf("binding = %+v, want demo/RoleBinding/api-can-list-cm", b.Binding)
	}
	if b.Role.Kind != "Role" || b.Role.Name != "list-cm" {
		t.Errorf("role = %+v, want demo/Role/list-cm", b.Role)
	}
	if len(b.Rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(b.Rules))
	}
	if len(b.Rules[0].Verbs) != 2 || b.Rules[0].Verbs[0] != "get" || b.Rules[0].Verbs[1] != "list" {
		t.Errorf("verbs = %+v, want [get list]", b.Rules[0].Verbs)
	}
}

// TestRBAC_RoleSubjects verifies the Role -> RB -> Subjects walk.
func TestRBAC_RoleSubjects(t *testing.T) {
	addr, cleanup := seedRBACFixture(t)
	defer cleanup()

	resp, body := getJSON(t,
		addr+"/api/v1alpha1/rbac/role/demo/list-cm/subjects", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	var got struct {
		Role struct {
			Kind, Namespace, Name string
		}
		Bindings []struct {
			Binding struct {
				Kind, Namespace, Name string
			}
			Subjects []struct {
				Kind, Namespace, Name string
			}
		}
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v\nbody=%s", err, body)
	}
	if got.Role.Kind != "Role" || got.Role.Name != "list-cm" {
		t.Errorf("role = %+v, want Role list-cm", got.Role)
	}
	if len(got.Bindings) != 1 {
		t.Fatalf("bindings = %d, want 1", len(got.Bindings))
	}
	subs := got.Bindings[0].Subjects
	if len(subs) != 1 || subs[0].Name != "api-sa" || subs[0].Namespace != "demo" {
		t.Errorf("subjects = %+v, want one demo/api-sa", subs)
	}
}

// TestRBAC_ClusterRoleSubjects exercises the cluster-scoped twin of
// the Role subjects handler.
func TestRBAC_ClusterRoleSubjects(t *testing.T) {
	addr, cleanup := seedRBACFixture(t)
	defer cleanup()

	resp, body := getJSON(t,
		addr+"/api/v1alpha1/rbac/clusterrole/cluster-admin/subjects", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	var got struct {
		Role struct {
			Kind, Namespace, Name string
		}
		Bindings []struct {
			Binding struct {
				Kind string
			}
			Subjects []struct {
				Kind, Namespace, Name string
			}
		}
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v\nbody=%s", err, body)
	}
	if got.Role.Kind != "ClusterRole" || got.Role.Name != "cluster-admin" {
		t.Errorf("role = %+v, want ClusterRole cluster-admin", got.Role)
	}
	if len(got.Bindings) != 1 || got.Bindings[0].Binding.Kind != "ClusterRoleBinding" {
		t.Errorf("bindings = %+v, want one ClusterRoleBinding", got.Bindings)
	}
	subs := got.Bindings[0].Subjects
	if len(subs) != 1 || subs[0].Name != "deployer" || subs[0].Namespace != "ops" {
		t.Errorf("subjects = %+v, want one ops/deployer", subs)
	}
}
