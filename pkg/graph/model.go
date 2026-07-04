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

	// ClusterID is the federation tag (P3-T20). Empty in single-
	// cluster mode (the default through v1.2) and set by the
	// multi-cluster informer manager (P3-T21) to the operator's
	// configured cluster name. Filters in
	// GraphStore.ListResourcesInCluster and
	// GraphStore.GetEdgesAcrossClusters key on it.
	//
	// v1alpha1 freezes its surface, so this field is v1 only — the
	// v1alpha1 marshaller drops it.
	ClusterID string `json:"clusterId,omitempty"`
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
//
// Single-cluster (ClusterID==""), the format is the v1.2 baseline:
// <namespace>/<kind>/<name>; namespace is empty for cluster-scoped
// resources (e.g. "/Namespace/demo"). Multi-cluster (ClusterID set)
// prepends <clusterID>: so two clusters with the same
// (namespace, kind, name) do not collide in the shared store
// (P3-T21). The colon is not a legal kubeconfig context character,
// so the prefix is unambiguous.
func (r Resource) ID() string {
	base := r.Namespace + "/" + r.Kind + "/" + r.Name
	if r.ClusterID == "" {
		return base
	}
	return r.ClusterID + ":" + base
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

	// Phase 2 P2-T14 RBAC edges. RoleBinding / ClusterRoleBinding
	// produce two edges each: one to every subject (SA / User /
	// Group) and one to the bound Role / ClusterRole. The store
	// accepts edges to User / Group endpoints even though no
	// informer creates those resource nodes — UI consumers decide
	// whether to render dangling endpoints.
	EdgeTypeBindsSubject EdgeType = "BINDS_SUBJECT"
	EdgeTypeBindsRole    EdgeType = "BINDS_ROLE"

	// Phase 3 P3-T1 NetworkPolicy edges (F-109).
	//
	// EdgeTypeSelectsNP is named with the _NP suffix to disambiguate
	// from EdgeTypeSelects which carries Service.spec.selector ->
	// Pod semantics. NetworkPolicy.spec.podSelector matches Pods in
	// the same namespace; the edge from the NetworkPolicy to each
	// matched Pod uses this type.
	//
	// EdgeTypeAllowsFrom / EdgeTypeAllowsTo model spec.ingress[].from
	// and spec.egress[].to. The edges describe declared traffic
	// permissions only — KubeAtlas reflects what the spec says, not
	// what the CNI actually enforces (anti-pattern guard:
	// modelling NetworkPolicy *effects* belongs to the CNI, not
	// KubeAtlas; see invariant on "topology, not runtime").
	EdgeTypeSelectsNP  EdgeType = "SELECTS_NP"
	EdgeTypeAllowsFrom EdgeType = "ALLOWS_FROM"
	EdgeTypeAllowsTo   EdgeType = "ALLOWS_TO"

	// Phase 3 F-209 platform-identity edges. KubeAtlas does NOT call
	// any cloud SDK (invariant 2.7); the edges are derived purely
	// from K8s metadata the platform's identity webhook writes —
	// EKS IRSA annotations, AKS Workload Identity labels, GKE
	// Workload Identity annotations. The To endpoint is a synthetic
	// "ExternalIdentity" id that no informer creates as a resource
	// row; UI consumers decide whether to render dangling endpoints
	// (same convention as RBAC User / Group subjects).
	EdgeTypeBindsPlatformIdentity EdgeType = "BINDS_PLATFORM_IDENTITY"

	// EdgeTypeScales connects an autoscaler to the workload it
	// scales. Source is a HorizontalPodAutoscaler (and, when added
	// later, VerticalPodAutoscaler); target is whatever resource
	// spec.scaleTargetRef names — typically a Deployment,
	// StatefulSet, or ReplicaSet. The edge describes a control-loop
	// relationship, not data flow, so the encoding sits in the
	// workload domain alongside OWNS.
	EdgeTypeScales EdgeType = "SCALES"

	// EdgeTypeEnforces connects an admission policy to a resource it
	// restricts: a Gatekeeper Constraint or a Kyverno (Cluster)Policy
	// on the From end, the matched/restricted resource on the To end.
	// Direction is policy -> restricted resource.
	//
	// It is deliberately NOT folded into SELECTS: a NetworkPolicy
	// SELECTS a Pod ("this policy picks that Pod"), whereas a
	// Constraint ENFORCES a Deployment ("this policy constrains that
	// Deployment") — different semantics, different colour in the UI.
	// Violation status rides on the edge's Attributes
	// (violated / violation_message).
	EdgeTypeEnforces EdgeType = "ENFORCES"
)

// EdgeTypeCallsAtRuntime is the F-204 OTel overlay's observed runtime
// call edge (caller workload -> callee workload), inferred from OTLP
// trace spans by the correlator (P5-T5).
//
// It is deliberately NOT a member of AllEdgeTypes and is never written
// to the declarative graph store: runtime edges are an opt-in overlay
// persisted to the Tier 2 otel_runtime_edges table and served ONLY by
// GET /api/v1/otel/overlay. Keeping it off AllEdgeTypes is what keeps
// /api/v1/graph and /api/v1alpha1/graph byte-identical to v1.4
// (invariant 2.2) and keeps CALLS_AT_RUNTIME a distinct type from the
// declarative ROUTES_TO (invariant 2.5): no extractor is registered
// for it, cypher.go never stores it, and the OpenAPI edge-type enum
// never lists it.
const EdgeTypeCallsAtRuntime EdgeType = "CALLS_AT_RUNTIME"

// AllEdgeTypes is the canonical edge-type list. Adding a new type
// means: add the constant above, append to this slice, write an
// extractor in pkg/extractor (or a Rego rule in
// lithastra/kubeatlas-rules), and document it in
// docs/architecture.md.
var AllEdgeTypes = []EdgeType{
	EdgeTypeOwns,
	EdgeTypeUsesConfigMap,
	EdgeTypeUsesSecret,
	EdgeTypeMountsVolume,
	EdgeTypeSelects,
	EdgeTypeUsesServiceAccount,
	EdgeTypeRoutesTo,
	EdgeTypeAttachedTo,
	EdgeTypeBindsSubject,
	EdgeTypeBindsRole,
	EdgeTypeSelectsNP,
	EdgeTypeAllowsFrom,
	EdgeTypeAllowsTo,
	EdgeTypeBindsPlatformIdentity,
	EdgeTypeScales,
	EdgeTypeEnforces,
}

// Edge represents a directed dependency between two resources.
type Edge struct {
	From string   `json:"from"` // Resource ID
	To   string   `json:"to"`   // Resource ID
	Type EdgeType `json:"type"` // strongly typed; one of AllEdgeTypes

	// Attributes carries optional, edge-type-specific metadata.
	// ENFORCES edges use it for Gatekeeper/Kyverno violation status
	// ("violated", "violation_message"); most edge types leave it
	// empty. Append-only on the JSON wire (omitted when empty) so it
	// does not change the shape existing consumers parse.
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Graph is a snapshot of the dependency graph.
type Graph struct {
	Resources []Resource `json:"resources"`
	Edges     []Edge     `json:"edges"`
}
