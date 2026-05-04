package graph

import "k8s.io/apimachinery/pkg/types"

// Resource represents a K8s resource instance.
type Resource struct {
	// PoC fields. Do not rename or reorder: external scripts and PR
	// reviews from the PoC era reference these names.
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels,omitempty"`

	// Phase 0 W2 additions. New fields are append-only to preserve the
	// JSON shape that PoC consumers may have parsed.
	GroupVersion    string            `json:"groupVersion,omitempty"`
	UID             types.UID         `json:"uid,omitempty"`
	Annotations     map[string]string `json:"annotations,omitempty"`
	OwnerReferences []OwnerRef        `json:"ownerReferences,omitempty"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`

	// Raw carries the full unstructured object as it came from the
	// apiserver. Extractors (pkg/extractor) read spec-level fields
	// from it. Marked json:"-" because it duplicates the structured
	// fields above and would double the serialized payload — clients
	// don't need it on the wire.
	Raw map[string]any `json:"-"`
}

// OwnerRef captures the K8s metadata.ownerReferences entry that the
// graph cares about. We deliberately do not include APIVersion or the
// Controller bool: KubeAtlas resolves owner edges via UID, and the
// Kind+Name+UID triple is enough.
type OwnerRef struct {
	Kind string    `json:"kind"`
	Name string    `json:"name"`
	UID  types.UID `json:"uid"`
}

// ID returns the resource's unique identifier within KubeAtlas.
// Format: <namespace>/<kind>/<name>; namespace is empty for cluster-
// scoped resources (e.g. "/Namespace/demo").
func (r Resource) ID() string {
	return r.Namespace + "/" + r.Kind + "/" + r.Name
}

// EdgeType is the strongly-typed enumeration of supported edge kinds.
// Underlying type is string for ergonomic JSON output and log
// readability.
type EdgeType string

const (
	EdgeTypeOwns               EdgeType = "OWNS"
	EdgeTypeUsesConfigMap      EdgeType = "USES_CONFIGMAP"
	EdgeTypeUsesSecret         EdgeType = "USES_SECRET"
	EdgeTypeMountsVolume       EdgeType = "MOUNTS_VOLUME"
	EdgeTypeSelects            EdgeType = "SELECTS"
	EdgeTypeUsesServiceAccount EdgeType = "USES_SERVICEACCOUNT"
	EdgeTypeRoutesTo           EdgeType = "ROUTES_TO"
	EdgeTypeAttachedTo         EdgeType = "ATTACHED_TO"
)

// AllEdgeTypes is the canonical Phase 0 list. Adding a new type means:
// add the constant above, append to this slice, write an extractor in
// pkg/extractor, and document it in docs/architecture.md.
var AllEdgeTypes = []EdgeType{
	EdgeTypeOwns,
	EdgeTypeUsesConfigMap,
	EdgeTypeUsesSecret,
	EdgeTypeMountsVolume,
	EdgeTypeSelects,
	EdgeTypeUsesServiceAccount,
	EdgeTypeRoutesTo,
	EdgeTypeAttachedTo,
}

// Edge represents a directed dependency between two resources.
type Edge struct {
	From string   `json:"from"` // Resource ID
	To   string   `json:"to"`   // Resource ID
	Type EdgeType `json:"type"` // strongly typed; one of AllEdgeTypes
}

// Graph is a snapshot of the dependency graph.
type Graph struct {
	Resources []Resource `json:"resources"`
	Edges     []Edge     `json:"edges"`
}
