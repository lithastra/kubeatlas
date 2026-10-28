package extractor

import (
	"context"
	"errors"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// BindsSubjectExtractor emits one BINDS_SUBJECT edge per entry in a
// RoleBinding / ClusterRoleBinding's `subjects` list. ServiceAccount
// subjects always resolve to a graph node ServiceAccountExtractor
// also references; User and Group subjects are emitted as edges to
// dangling endpoints (no informer creates User / Group resources).
// UI consumers decide whether to render those endpoints.
// Non-null subjects must be an array of objects. Malformed entries
// retain earlier subjects and return an error before later entries.
// For the three supported subject kinds, explicit apiGroup values must
// match Kubernetes defaulting/validation. A bad group retains earlier
// subjects and returns an error before later entries are processed.
//
// Subject namespace handling:
//
//   - ServiceAccount in a RoleBinding: subject.namespace defaults to
//     the RoleBinding's own namespace when absent/null/empty, matching
//     Kubernetes' same-namespace subject resolution. Wrong types return
//     an error instead of taking that fallback.
//   - ServiceAccount in a ClusterRoleBinding: subject.namespace is
//     mandatory. An unresolved namespace returns an extraction error
//     with the confirmed subject prefix, not a namespace-less edge.
//   - User / Group: legacy subject.namespace handling is unchanged.
type BindsSubjectExtractor struct{}

func (BindsSubjectExtractor) Type() graph.EdgeType { return graph.EdgeTypeBindsSubject }

func (BindsSubjectExtractor) Extract(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
	if r.Kind != "RoleBinding" && r.Kind != "ClusterRoleBinding" {
		return nil, nil
	}
	value := r.Raw["subjects"]
	if value == nil {
		return nil, nil
	}
	subjects, ok := value.([]any)
	if !ok {
		return nil, errors.New("BindsSubjectExtractor: subjects must be an array")
	}
	if len(subjects) == 0 {
		return nil, nil
	}
	out := make([]graph.Edge, 0, len(subjects))
	for _, s := range subjects {
		sm, ok := s.(map[string]any)
		if !ok || sm == nil {
			return out, errors.New("BindsSubjectExtractor: subject must be an object")
		}
		kind, _ := sm["kind"].(string)
		name, _ := sm["name"].(string)
		if kind == "" || name == "" {
			continue
		}
		switch kind {
		case "ServiceAccount", "User", "Group":
			if value, present := sm["apiGroup"]; present {
				group, ok := value.(string)
				if !ok || group != "" && (kind == "ServiceAccount" || group != "rbac.authorization.k8s.io") {
					return out, errors.New("BindsSubjectExtractor: unsupported or malformed subject apiGroup")
				}
			}
		}
		ns, stringNamespace := sm["namespace"].(string)
		if kind == "ServiceAccount" {
			if sm["namespace"] != nil && !stringNamespace {
				return out, errors.New("BindsSubjectExtractor: malformed ServiceAccount namespace")
			}
			if ns == "" && r.Kind == "RoleBinding" {
				ns = r.Namespace
			}
			if ns == "" {
				return out, errors.New("BindsSubjectExtractor: missing ServiceAccount namespace")
			}
		}
		out = append(out, graph.Edge{
			From: r.ID(),
			To:   graph.Resource{Kind: kind, Namespace: ns, Name: name, ClusterID: r.ClusterID}.ID(),
			Type: graph.EdgeTypeBindsSubject,
		})
	}
	return out, nil
}

// BindsRoleExtractor emits a single BINDS_ROLE edge from a binding to
// the Role / ClusterRole referenced by `roleRef`. RoleBinding can
// reference EITHER Role (same namespace) OR ClusterRole (cluster-
// scoped); ClusterRoleBinding must reference ClusterRole. Omitted/empty
// roleRef.apiGroup retains RBAC defaulting; explicit values must name that group.
// A non-null roleRef must be an object; wrong container types return an error.
type BindsRoleExtractor struct{}

func (BindsRoleExtractor) Type() graph.EdgeType { return graph.EdgeTypeBindsRole }

func (BindsRoleExtractor) Extract(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
	if r.Kind != "RoleBinding" && r.Kind != "ClusterRoleBinding" {
		return nil, nil
	}
	value := r.Raw["roleRef"]
	if value == nil {
		return nil, nil
	}
	ref, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("BindsRoleExtractor: roleRef must be an object")
	}
	kind, _ := ref["kind"].(string)
	name, _ := ref["name"].(string)
	if kind == "" || name == "" {
		return nil, nil
	}
	if value, present := ref["apiGroup"]; present {
		group, ok := value.(string)
		if !ok || group != "" && group != "rbac.authorization.k8s.io" {
			return nil, errors.New("BindsRoleExtractor: unsupported or malformed roleRef apiGroup")
		}
	}
	// ClusterRole is cluster-scoped — empty namespace produces the
	// "/ClusterRole/<name>" ID. Role lives in the binding's
	// namespace; ClusterRoleBinding cannot reference Role at all
	// (the API forbids it), but we don't enforce that here — a
	// malformed CRB with kind=Role would emit a dangling edge.
	targetNS := ""
	if kind == "Role" {
		targetNS = r.Namespace
	}
	return []graph.Edge{{
		From: r.ID(),
		To:   graph.Resource{Kind: kind, Namespace: targetNS, Name: name, ClusterID: r.ClusterID}.ID(),
		Type: graph.EdgeTypeBindsRole,
	}}, nil
}
