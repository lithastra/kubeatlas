// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// handleRBACServiceAccountPermissions walks the SA's BINDS_SUBJECT
// incoming edges back through RoleBinding / ClusterRoleBinding to
// the bound Role / ClusterRole, then returns each role's rules
// block. The shape is intentionally close to a "kubectl auth can-i
// --list" output so existing operator muscle memory carries over.
func (s *Server) handleRBACServiceAccountPermissions(w http.ResponseWriter, r *http.Request) {
	namespace, name, ok := rbacPath(r, true)
	if !ok {
		http.Error(w, "namespace and name are required", http.StatusBadRequest)
		return
	}
	saID := graph.Resource{Kind: "ServiceAccount", Namespace: namespace, Name: name}.ID()

	bindings, err := s.bindingsForSubject(r.Context(), saID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := rbacPermissionsResponse{
		Subject:  rbacSubjectRef{Kind: "ServiceAccount", Namespace: namespace, Name: name},
		Bindings: bindings,
	}
	writeRBACJSON(w, resp)
}

// handleRBACRoleSubjects walks a namespaced Role's BINDS_ROLE
// incoming edges back through the bindings to the subjects each
// binding lists.
func (s *Server) handleRBACRoleSubjects(w http.ResponseWriter, r *http.Request) {
	namespace, name, ok := rbacPath(r, false)
	if !ok {
		http.Error(w, "namespace and name are required", http.StatusBadRequest)
		return
	}
	role := graph.Resource{Kind: "Role", Namespace: namespace, Name: name}
	bindings, err := s.bindingsForRole(r.Context(), role.ID())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeRBACJSON(w, rbacSubjectsResponse{
		Role:     rbacSubjectRef{Kind: role.Kind, Namespace: role.Namespace, Name: role.Name},
		Bindings: bindings,
	})
}

// handleRBACClusterRoleSubjects is the cluster-scoped twin of the
// Role handler. Lives on its own route because net/http's mux folds
// repeated slashes, so an empty-namespace path on the Role route
// would 404.
func (s *Server) handleRBACClusterRoleSubjects(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	role := graph.Resource{Kind: "ClusterRole", Namespace: "", Name: name}
	bindings, err := s.bindingsForRole(r.Context(), role.ID())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeRBACJSON(w, rbacSubjectsResponse{
		Role:     rbacSubjectRef{Kind: role.Kind, Namespace: role.Namespace, Name: role.Name},
		Bindings: bindings,
	})
}

// rbacPath pulls the namespace + name path values out of the
// request. namespacedDefault=true accepts an empty namespace for
// SA convenience (most callers pass a real namespace anyway).
func rbacPath(r *http.Request, _ bool) (namespace, name string, ok bool) {
	namespace = r.PathValue("namespace")
	name = r.PathValue("name")
	if name == "" {
		return "", "", false
	}
	return namespace, name, true
}

// bindingsForSubject finds every RoleBinding / ClusterRoleBinding
// that lists subjectID under BINDS_SUBJECT, then for each such
// binding follows BINDS_ROLE forward to the role and pulls its
// rules block.
func (s *Server) bindingsForSubject(ctx context.Context, subjectID string) ([]rbacBinding, error) {
	incoming, err := s.store.ListEdges(ctx, subjectID, graph.DirectionIncoming)
	if err != nil {
		return nil, err
	}
	var out []rbacBinding
	for _, e := range incoming {
		if e.Type != graph.EdgeTypeBindsSubject {
			continue
		}
		bindingID := e.From
		binding, err := s.store.GetResource(ctx, bindingID)
		if err != nil {
			// Dangling edge — binding was deleted between our
			// listing and lookup. Skip without erroring out the
			// whole response.
			if errors.As(err, new(graph.ErrNotFound)) {
				continue
			}
			return nil, err
		}
		role, rules, err := s.roleForBinding(ctx, bindingID)
		if err != nil {
			return nil, err
		}
		out = append(out, rbacBinding{
			Binding: rbacSubjectRef{
				Kind:      binding.Kind,
				Namespace: binding.Namespace,
				Name:      binding.Name,
			},
			Role:  role,
			Rules: rules,
		})
	}
	return out, nil
}

// bindingsForRole walks BINDS_ROLE incoming edges from the role
// back to the bindings, then collects each binding's subjects.
func (s *Server) bindingsForRole(ctx context.Context, roleID string) ([]rbacBinding, error) {
	incoming, err := s.store.ListEdges(ctx, roleID, graph.DirectionIncoming)
	if err != nil {
		return nil, err
	}
	var out []rbacBinding
	for _, e := range incoming {
		if e.Type != graph.EdgeTypeBindsRole {
			continue
		}
		bindingID := e.From
		binding, err := s.store.GetResource(ctx, bindingID)
		if err != nil {
			if errors.As(err, new(graph.ErrNotFound)) {
				continue
			}
			return nil, err
		}
		subjects, err := s.subjectsForBinding(ctx, bindingID)
		if err != nil {
			return nil, err
		}
		out = append(out, rbacBinding{
			Binding: rbacSubjectRef{
				Kind:      binding.Kind,
				Namespace: binding.Namespace,
				Name:      binding.Name,
			},
			Subjects: subjects,
		})
	}
	return out, nil
}

// roleForBinding follows the binding's outgoing BINDS_ROLE edge to
// the bound Role / ClusterRole, then reads .rules off the role's
// Raw payload. Returns (zero, nil, nil) when the binding has no
// role edge — that's a transient state during informer sync, not
// an error.
func (s *Server) roleForBinding(ctx context.Context, bindingID string) (rbacSubjectRef, []rbacRule, error) {
	outgoing, err := s.store.ListEdges(ctx, bindingID, graph.DirectionOutgoing)
	if err != nil {
		return rbacSubjectRef{}, nil, err
	}
	for _, e := range outgoing {
		if e.Type != graph.EdgeTypeBindsRole {
			continue
		}
		role, err := s.store.GetResource(ctx, e.To)
		if err != nil {
			if errors.As(err, new(graph.ErrNotFound)) {
				continue
			}
			return rbacSubjectRef{}, nil, err
		}
		return rbacSubjectRef{
			Kind: role.Kind, Namespace: role.Namespace, Name: role.Name,
		}, extractRules(role), nil
	}
	return rbacSubjectRef{}, nil, nil
}

// subjectsForBinding follows the binding's outgoing BINDS_SUBJECT
// edges to the subject endpoints. Some edges point at User / Group
// nodes that no informer materialises — we still emit them since
// the consumer might want to display "alice" alongside SA bindings.
func (s *Server) subjectsForBinding(ctx context.Context, bindingID string) ([]rbacSubjectRef, error) {
	outgoing, err := s.store.ListEdges(ctx, bindingID, graph.DirectionOutgoing)
	if err != nil {
		return nil, err
	}
	var out []rbacSubjectRef
	for _, e := range outgoing {
		if e.Type != graph.EdgeTypeBindsSubject {
			continue
		}
		// Decode the subject ID directly (namespace/Kind/name)
		// rather than fetch the resource — User / Group endpoints
		// have no resource node by design.
		subj := decodeResourceID(e.To)
		out = append(out, rbacSubjectRef{
			Kind: subj.Kind, Namespace: subj.Namespace, Name: subj.Name,
		})
	}
	return out, nil
}

// extractRules pulls the .rules array off a Role / ClusterRole's
// Raw map, normalising into a typed rbacRule slice. Anything that
// isn't a well-shaped rule is skipped — RBAC quirks (e.g. *
// resource lists) come through verbatim because we just copy the
// strings.
func extractRules(role graph.Resource) []rbacRule {
	if role.Raw == nil {
		return nil
	}
	rawRules, ok := role.Raw["rules"].([]any)
	if !ok {
		return nil
	}
	out := make([]rbacRule, 0, len(rawRules))
	for _, rr := range rawRules {
		rm, ok := rr.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, rbacRule{
			APIGroups:     stringSlice(rm["apiGroups"]),
			Resources:     stringSlice(rm["resources"]),
			ResourceNames: stringSlice(rm["resourceNames"]),
			Verbs:         stringSlice(rm["verbs"]),
		})
	}
	return out
}

func stringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// decodeResourceID reverses graph.Resource.ID() — turns
// "namespace/Kind/name" back into the three fields. Empty
// namespace decodes correctly because the prefix is "".
func decodeResourceID(id string) graph.Resource {
	// IDs are exactly two slashes; the namespace half may be empty.
	first := -1
	second := -1
	for i, c := range id {
		if c == '/' {
			if first == -1 {
				first = i
			} else {
				second = i
				break
			}
		}
	}
	if first == -1 || second == -1 {
		return graph.Resource{Name: id}
	}
	return graph.Resource{
		Namespace: id[:first],
		Kind:      id[first+1 : second],
		Name:      id[second+1:],
	}
}

// writeRBACJSON is a 200-only JSON serializer for the RBAC
// handlers. The package's other writeJSON takes (w, status, v) and
// is used by handlers that branch on status codes; the RBAC
// handlers always return 200 on the happy path and rely on
// http.Error for failure, so a simpler shape keeps the call sites
// readable.
func writeRBACJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// ----- response shapes ------------------------------------------------

type rbacSubjectRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

type rbacRule struct {
	APIGroups     []string `json:"apiGroups,omitempty"`
	Resources     []string `json:"resources,omitempty"`
	ResourceNames []string `json:"resourceNames,omitempty"`
	Verbs         []string `json:"verbs,omitempty"`
}

type rbacBinding struct {
	Binding  rbacSubjectRef   `json:"binding"`
	Role     rbacSubjectRef   `json:"role,omitempty"`
	Rules    []rbacRule       `json:"rules,omitempty"`
	Subjects []rbacSubjectRef `json:"subjects,omitempty"`
}

type rbacPermissionsResponse struct {
	Subject  rbacSubjectRef `json:"subject"`
	Bindings []rbacBinding  `json:"bindings"`
}

type rbacSubjectsResponse struct {
	Role     rbacSubjectRef `json:"role"`
	Bindings []rbacBinding  `json:"bindings"`
}
