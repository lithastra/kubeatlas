package extractor

import (
	"context"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// BindsSubjectExtractor emits one BINDS_SUBJECT edge per entry in a
// RoleBinding / ClusterRoleBinding's `subjects` list. ServiceAccount
// subjects always resolve to a graph node ServiceAccountExtractor
// also references; User and Group subjects are emitted as edges to
// dangling endpoints (no informer creates User / Group resources).
// UI consumers decide whether to render those endpoints.
//
// Subject namespace handling:
//
//   - ServiceAccount in a RoleBinding: subject.namespace defaults to
//     the RoleBinding's own namespace when absent. The K8s API
//     enforces this defaulting; we replay it for clarity.
//   - ServiceAccount in a ClusterRoleBinding: subject.namespace is
//     mandatory. We trust the API server: if absent the edge would
//     point at "/ServiceAccount/<name>" which is invalid and
//     dangling — better surfaced than silently dropped.
//   - User / Group: subject.namespace is meaningless; the resulting
//     ID is "/User/<name>" (cluster-scoped notation).
type BindsSubjectExtractor struct{}

func (BindsSubjectExtractor) Type() graph.EdgeType { return graph.EdgeTypeBindsSubject }

func (BindsSubjectExtractor) Extract(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
	if r.Kind != "RoleBinding" && r.Kind != "ClusterRoleBinding" {
		return nil, nil
	}
	subjects := nestedSliceTop(r.Raw, "subjects")
	if len(subjects) == 0 {
		return nil, nil
	}
	out := make([]graph.Edge, 0, len(subjects))
	for _, s := range subjects {
		sm, ok := s.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := sm["kind"].(string)
		name, _ := sm["name"].(string)
		if kind == "" || name == "" {
			continue
		}
		ns, _ := sm["namespace"].(string)
		if kind == "ServiceAccount" && ns == "" && r.Kind == "RoleBinding" {
			ns = r.Namespace
		}
		out = append(out, graph.Edge{
			From: r.ID(),
			To:   graph.Resource{Kind: kind, Namespace: ns, Name: name}.ID(),
			Type: graph.EdgeTypeBindsSubject,
		})
	}
	return out, nil
}

// BindsRoleExtractor emits a single BINDS_ROLE edge from a binding to
// the Role / ClusterRole referenced by `roleRef`. RoleBinding can
// reference EITHER Role (same namespace) OR ClusterRole (cluster-
// scoped); ClusterRoleBinding must reference ClusterRole.
type BindsRoleExtractor struct{}

func (BindsRoleExtractor) Type() graph.EdgeType { return graph.EdgeTypeBindsRole }

func (BindsRoleExtractor) Extract(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
	if r.Kind != "RoleBinding" && r.Kind != "ClusterRoleBinding" {
		return nil, nil
	}
	ref := nestedMapTop(r.Raw, "roleRef")
	if ref == nil {
		return nil, nil
	}
	kind, _ := ref["kind"].(string)
	name, _ := ref["name"].(string)
	if kind == "" || name == "" {
		return nil, nil
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
		To:   graph.Resource{Kind: kind, Namespace: targetNS, Name: name}.ID(),
		Type: graph.EdgeTypeBindsRole,
	}}, nil
}

// nestedSliceTop is the top-level twin of nestedSlice: RoleBinding's
// `subjects` and `roleRef` live at the resource root, not under
// `spec`, so the existing nestedSlice (which always descends through
// nested keys) is awkward to use. Returns nil when the path is
// absent or the wrong type.
func nestedSliceTop(raw map[string]any, key string) []any {
	if raw == nil {
		return nil
	}
	v, ok := raw[key].([]any)
	if !ok {
		return nil
	}
	return v
}

// nestedMapTop is the top-level twin of nestedMap.
func nestedMapTop(raw map[string]any, key string) map[string]any {
	if raw == nil {
		return nil
	}
	v, ok := raw[key].(map[string]any)
	if !ok {
		return nil
	}
	return v
}
