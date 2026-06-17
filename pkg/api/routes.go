package api

import "net/http"

// RouteInfo describes one HTTP route the Server exposes. The Server
// uses this list in two places:
//
//   - registerRoutes binds each entry to its ServeMux, so the route
//     table is the single source of truth for "what the server serves".
//   - The OpenAPI spec generator walks the same list, so the spec
//     can't drift from the registered routes by construction.
//
// The handler field is intentionally unexported: external callers
// (tests, the OpenAPI generator) read metadata, internal code reads
// the handler. Public fields are append-only across versions.
type RouteInfo struct {
	Method      string      // "GET"
	Pattern     string      // "/healthz", "/api/v1alpha1/graph", etc.
	Summary     string      // one-line summary (also used as OpenAPI summary)
	Description string      // longer description, used in OpenAPI description
	PathParams  []ParamSpec // path-template variables (in: path)
	QueryParams []ParamSpec // query-string params  (in: query)
	Response    ResponseSpec

	handler http.HandlerFunc
}

// ParamSpec is the metadata an OpenAPI parameter needs.
type ParamSpec struct {
	Name        string
	Required    bool
	Description string
	Type        string   // "string" | "integer"
	Enum        []string // optional restricted values; nil = unrestricted
}

// ResponseSpec is the metadata for the success response.
type ResponseSpec struct {
	Description string
	SchemaRef   string // "" = no schema; non-empty = "#/components/schemas/<ref>"
	ContentType string // default "application/json"
}

// Routes returns the ordered list of routes the Server registers.
// Tests use this to assert the OpenAPI spec covers every endpoint.
func (s *Server) Routes() []RouteInfo {
	return []RouteInfo{
		{
			Method: "GET", Pattern: "/healthz",
			Summary:     "Liveness probe",
			Description: "Returns 200 OK while the process can serve HTTP. Never gates on cluster state.",
			Response:    ResponseSpec{Description: "Process is alive", SchemaRef: "HealthResponse"},
			handler:     s.handleHealth,
		},
		{
			Method: "GET", Pattern: "/readyz",
			Summary:     "Readiness probe",
			Description: "Returns 200 once the informer cache has completed initial sync; 503 until then.",
			Response:    ResponseSpec{Description: "Process is ready to serve traffic", SchemaRef: "HealthResponse"},
			handler:     s.handleReady,
		},
		{
			Method: "GET", Pattern: "/metrics",
			Summary:     "Prometheus metrics",
			Description: "Hand-rolled exposition covering goroutine count, informer sync state, and request counts by method/status.",
			Response:    ResponseSpec{Description: "Prometheus text exposition", ContentType: "text/plain"},
			handler:     s.handleMetrics,
		},
		{
			Method: "GET", Pattern: "/api/v1alpha1/graph",
			Summary:     "Graph at a given level",
			Description: "Returns an aggregated View at one of four levels: cluster, namespace, workload, resource. Workload and resource scopes require namespace+kind+name; namespace requires namespace.",
			QueryParams: []ParamSpec{
				{Name: "level", Required: true, Description: "Aggregation level", Type: "string",
					Enum: []string{"cluster", "namespace", "workload", "resource"}},
				{Name: "namespace", Description: "Namespace; required for namespace/workload/resource", Type: "string"},
				{Name: "kind", Description: "Resource Kind; required for workload/resource", Type: "string"},
				{Name: "name", Description: "Resource name; required for workload/resource", Type: "string"},
			},
			Response: ResponseSpec{Description: "Aggregated graph view", SchemaRef: "View"},
			handler:  s.handleGraph,
		},
		{
			Method: "GET", Pattern: "/api/v1alpha1/resources/{namespace}/{kind}/{name}",
			Summary:     "Single-resource detail with edges",
			Description: "Returns the resource plus its incoming and outgoing edges in one round-trip. Use the underscore '_' as the namespace for cluster-scoped resources.",
			PathParams: []ParamSpec{
				{Name: "namespace", Required: true, Description: "Namespace (use '_' for cluster-scoped)", Type: "string"},
				{Name: "kind", Required: true, Description: "Resource Kind", Type: "string"},
				{Name: "name", Required: true, Description: "Resource name", Type: "string"},
			},
			Response: ResponseSpec{Description: "Resource detail bundle", SchemaRef: "ResourceDetailResponse"},
			handler:  s.handleResource,
		},
		{
			Method: "GET", Pattern: "/api/v1alpha1/resources/{namespace}/{kind}/{name}/incoming",
			Summary:     "Incoming edges for a resource",
			Description: "Returns every edge whose To field equals this resource's id.",
			PathParams: []ParamSpec{
				{Name: "namespace", Required: true, Type: "string"},
				{Name: "kind", Required: true, Type: "string"},
				{Name: "name", Required: true, Type: "string"},
			},
			Response: ResponseSpec{Description: "Edge list", SchemaRef: "EdgeList"},
			handler:  s.handleIncoming,
		},
		{
			Method: "GET", Pattern: "/api/v1alpha1/resources/{namespace}/{kind}/{name}/outgoing",
			Summary:     "Outgoing edges for a resource",
			Description: "Returns every edge whose From field equals this resource's id.",
			PathParams: []ParamSpec{
				{Name: "namespace", Required: true, Type: "string"},
				{Name: "kind", Required: true, Type: "string"},
				{Name: "name", Required: true, Type: "string"},
			},
			Response: ResponseSpec{Description: "Edge list", SchemaRef: "EdgeList"},
			handler:  s.handleOutgoing,
		},
		{
			Method: "GET", Pattern: "/api/v1alpha1/search",
			Summary:     "Full-text search across resources",
			Description: "Ranked full-text search over name / kind / namespace / label values. On Tier 2 it runs as one GIN-indexed tsvector match; on Tier 1 it is a linear scan and the response carries a warning. P3-T8 (F-113).",
			QueryParams: []ParamSpec{
				{Name: "q", Required: true, Description: "Free-text terms plus optional kind:/namespace: filter tokens, e.g. 'customers kind:Pod'", Type: "string"},
				{Name: "limit", Description: "Max matches to return (default 50, max 200)", Type: "integer"},
			},
			Response: ResponseSpec{Description: "Search results", SchemaRef: "SearchResponse"},
			handler:  s.handleSearch,
		},
		{
			Method: "GET", Pattern: "/api/v1alpha1/labels",
			Summary:     "Label keys and their value distribution",
			Description: "Returns every label key present on any resource, how many resources carry it, and its most common values (capped per key). Powers the 'group by label' picker. P3-T9 (F-114).",
			Response:    ResponseSpec{Description: "Label key statistics", SchemaRef: "LabelsResponse"},
			handler:     s.handleLabels,
		},
		{
			Method: "GET", Pattern: "/api/v1alpha1/watch",
			Summary:     "WebSocket watch stream",
			Description: "Upgrades to WebSocket. Client first sends a SUBSCRIBE envelope; the server then streams GRAPH_UPDATE envelopes plus PING heartbeats every 30 seconds. See the protocol notes in the spec's `x-websocket` extension.",
			Response:    ResponseSpec{Description: "Upgrade to WebSocket (returns 101 Switching Protocols on the wire)"},
			handler:     s.hub.Handle,
		},
		{
			Method: "GET", Pattern: "/api/v1alpha1/openapi.json",
			Summary:     "OpenAPI 3.0 spec for this API",
			Description: "Hand-written, generated from the live route table. Used by the docs site and by external tooling.",
			Response:    ResponseSpec{Description: "OpenAPI 3.0 document", SchemaRef: "OpenAPI"},
			handler:     s.handleOpenAPI,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1alpha1/rbac/serviceaccount/{namespace}/{name}/permissions",
			Summary:     "RBAC: roles bound to a ServiceAccount",
			Description: "Walks BINDS_SUBJECT incoming edges on the SA back through RoleBinding / ClusterRoleBinding to the bound Role / ClusterRole and returns the rule rules block of each. Phase 2 P2-T14.",
			PathParams: []ParamSpec{
				{Name: "namespace", Required: true, Type: "string"},
				{Name: "name", Required: true, Type: "string"},
			},
			Response: ResponseSpec{Description: "Permissions summary", SchemaRef: "RBACPermissions"},
			handler:  s.handleRBACServiceAccountPermissions,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1alpha1/rbac/role/{namespace}/{name}/subjects",
			Summary:     "RBAC: subjects bound to a Role",
			Description: "Walks BINDS_ROLE incoming edges on the (namespaced) Role back through RoleBinding / ClusterRoleBinding to the subjects each binding lists. For ClusterRole use /clusterrole/{name}/subjects instead.",
			PathParams: []ParamSpec{
				{Name: "namespace", Required: true, Type: "string"},
				{Name: "name", Required: true, Type: "string"},
			},
			Response: ResponseSpec{Description: "Subject list", SchemaRef: "RBACSubjects"},
			handler:  s.handleRBACRoleSubjects,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1alpha1/rbac/clusterrole/{name}/subjects",
			Summary:     "RBAC: subjects bound to a ClusterRole",
			Description: "Same shape as the namespaced /role variant but for cluster-scoped ClusterRoles. Required as a separate route because net/http's mux folds repeated slashes, so a {namespace}-empty path 404s.",
			PathParams: []ParamSpec{
				{Name: "name", Required: true, Type: "string"},
			},
			Response: ResponseSpec{Description: "Subject list", SchemaRef: "RBACSubjects"},
			handler:  s.handleRBACClusterRoleSubjects,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1alpha1/cycles",
			Summary:     "Strongly connected components of size >= 2",
			Description: "Returns every SCC of two or more resources. In a healthy cluster the list is empty; non-empty results indicate cyclic dependencies (e.g. ConfigMaps that reference each other). P2-T18 (F-112 part 2).",
			Response:    ResponseSpec{Description: "Cycle reports", SchemaRef: "CyclesResponse"},
			handler:     s.handleCycles,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1alpha1/orphans",
			Summary:     "List resources with no upstream owner",
			Description: "Returns every resource that is either a non-top-level kind with zero incoming edges (an orphan) or a Pod without an OwnerReference (a standalone Pod). Optional query param `namespace` narrows the scope. P2-T17 (F-112 part 1).",
			QueryParams: []ParamSpec{
				{Name: "namespace", Description: "Restrict the sweep to one namespace", Type: "string"},
			},
			Response: ResponseSpec{Description: "Orphan reports", SchemaRef: "OrphansResponse"},
			handler:  s.handleOrphans,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1alpha1/snapshots",
			Summary:     "List recorded full-sync snapshot markers",
			Description: "Returns every snapshot_meta marker, most-recent first. Tier 2 only — a Tier 1 install (or snapshots.enabled=false) returns 503. P3-T5 (F-111).",
			Response:    ResponseSpec{Description: "Snapshot markers", SchemaRef: "SnapshotListResponse"},
			handler:     s.handleSnapshots,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1alpha1/snapshots/diff",
			Summary:     "Resource changes across a time window",
			Description: "Returns resources added / removed / modified between `from` and `to`. Times accept 'now', a duration ('5m'/'1h'/'7d', read as ago), or RFC3339. The window may not exceed the retention limit. Tier 2 only — Tier 1 returns 503. P3-T5 (F-111).",
			QueryParams: []ParamSpec{
				{Name: "from", Required: true, Description: "Window start: 'now', a duration ('5m'), or RFC3339", Type: "string"},
				{Name: "to", Description: "Window end; defaults to 'now'", Type: "string"},
				{Name: "namespace", Description: "Restrict the diff to one namespace; empty = whole cluster", Type: "string"},
			},
			Response: ResponseSpec{Description: "Diff result", SchemaRef: "DiffResult"},
			handler:  s.handleSnapshotDiff,
		},
		{
			Method:      "POST",
			Pattern:     "/api/_internal/snapshot/trigger",
			Summary:     "Record a full-sync snapshot marker",
			Description: "Writes one snapshot_meta row anchoring the diff endpoint to a known full-sync point. Internal: served only on the ClusterIP Service, never exposed through Ingress. The F-111 CronJob is the intended caller. P3-T4 (F-111).",
			QueryParams: []ParamSpec{
				{Name: "trigger", Description: "Marker kind: periodic (CronJob) or manual (operator). Defaults to manual.", Type: "string", Enum: []string{"periodic", "manual"}},
			},
			Response: ResponseSpec{Description: "Snapshot marker recorded", SchemaRef: "SnapshotTriggerResponse"},
			handler:  s.handleSnapshotTrigger,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1alpha1/networkpolicy/{namespace}/{name}/selected",
			Summary:     "Pods a NetworkPolicy's podSelector selects",
			Description: "Returns every Pod (and Pod-template-carrying workload) in the policy's namespace that spec.podSelector matches, resolved from the SELECTS_NP edges. P3-T1 (F-109).",
			PathParams: []ParamSpec{
				{Name: "namespace", Required: true, Type: "string"},
				{Name: "name", Required: true, Description: "NetworkPolicy name", Type: "string"},
			},
			Response: ResponseSpec{Description: "Selected pods", SchemaRef: "NetworkPolicySelectedResponse"},
			handler:  s.handleNetworkPolicySelected,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1alpha1/networkpolicy/{namespace}/{name}/allow-graph",
			Summary:     "Declared ingress sources and egress destinations of a NetworkPolicy",
			Description: "Returns the ALLOWS_FROM (spec.ingress[].from[]) and ALLOWS_TO (spec.egress[].to[]) targets — Pods, workloads, and Namespaces the policy declares as permitted peers. Declarative topology only; reflects the spec, not CNI enforcement. P3-T1 (F-109).",
			PathParams: []ParamSpec{
				{Name: "namespace", Required: true, Type: "string"},
				{Name: "name", Required: true, Description: "NetworkPolicy name", Type: "string"},
			},
			Response: ResponseSpec{Description: "Allow-from / allow-to subgraph", SchemaRef: "NetworkPolicyAllowGraphResponse"},
			handler:  s.handleNetworkPolicyAllowGraph,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1alpha1/blast-radius/{namespace}/{kind}/{name}",
			Summary:     "Transitive set of resources affected by changes to this resource",
			Description: "Returns every resource reachable by walking incoming edges from the target — i.e. everything that would be impacted if this resource were deleted or broken. Use the underscore '_' as namespace for cluster-scoped resources. P2-T15 (F-110).",
			PathParams: []ParamSpec{
				{Name: "namespace", Required: true, Description: "Namespace (use '_' for cluster-scoped)", Type: "string"},
				{Name: "kind", Required: true, Type: "string"},
				{Name: "name", Required: true, Type: "string"},
			},
			QueryParams: []ParamSpec{
				{Name: "max_depth", Description: "Path-length cap; default 5, hard max 10", Type: "integer"},
				{Name: "edge_types", Description: "Comma-separated edge labels to follow; empty = any", Type: "string"},
				{Name: "include_source", Description: "Include the start resource in the result set; default false", Type: "string"},
			},
			Response: ResponseSpec{Description: "Affected resources", SchemaRef: "BlastRadiusResponse"},
			handler:  s.handleBlastRadius,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1alpha1/export",
			Summary:     "Render a cluster or namespace view as an image",
			Description: "Server-side renders the dependency graph to SVG or PNG via Graphviz. Optional `namespace` narrows the view; the whole-cluster render is refused past 1000 nodes (413). Concurrency-limited (429 when busy); 503 when the renderer is unavailable. P3-T14 (F-115).",
			QueryParams: []ParamSpec{
				{Name: "format", Description: "Image format; defaults to svg", Type: "string", Enum: []string{"svg", "png"}},
				{Name: "namespace", Description: "Restrict the render to one namespace; empty = whole cluster", Type: "string"},
			},
			Response: ResponseSpec{Description: "Rendered SVG or PNG image", ContentType: "image/svg+xml"},
			handler:  s.handleExport,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1/info",
			Summary:     "Build metadata and internal store version",
			Description: "Returns the server version/commit/build-date and the internal GraphStore interface version (graphstore_version, e.g. \"v2\"). The store interface version is an internal engineering version, unrelated to the product release version or the v1alpha1/v1 HTTP API versions. v1-only.",
			Response:    ResponseSpec{Description: "Server build and store info", SchemaRef: "InfoResponse"},
			handler:     s.handleInfo,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1/diagnose",
			Summary:     "Self-contained diagnostic report",
			Description: "Bundles the scoped dependency graph plus orphan, cycle, and top blast-radius analysis into one report. format=json returns the structured data; format=html returns a self-contained HTML document (inline CSS, no external resources) for air-gapped audits. Optional `namespace` narrows the scope; empty = whole cluster. v1-only (Phase 4). P4-T1 (F-301).",
			QueryParams: []ParamSpec{
				{Name: "namespace", Description: "Restrict the report to one namespace; empty = whole cluster", Type: "string"},
				{Name: "format", Description: "Output format; defaults to json", Type: "string", Enum: []string{"json", "html"}},
			},
			Response: ResponseSpec{Description: "Diagnostic report", SchemaRef: "DiagnoseReport"},
			handler:  s.handleDiagnose,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1/policy/constraints",
			Summary:     "List admission-policy constraints",
			Description: "Returns every Gatekeeper Constraint and Kyverno (Cluster)Policy with its live violation count, read from the engine's status/reports — KubeAtlas observes the result and never re-evaluates the policy. Optional `engine` restricts the engine. v1-only.",
			QueryParams: []ParamSpec{
				{Name: "engine", Description: "Filter by policy engine; default returns all supported engines", Type: "string", Enum: []string{"gatekeeper", "kyverno"}},
			},
			Response: ResponseSpec{Description: "Constraint summaries", SchemaRef: "PolicyConstraintList"},
			handler:  s.handlePolicyConstraints,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1/policy/constraints/{name}/affected",
			Summary:     "Resources a constraint enforces",
			Description: "Returns the resources the named constraint enforces (from its ENFORCES edges), each flagged with the violation status the policy engine reported. v1-only.",
			PathParams: []ParamSpec{
				{Name: "name", Required: true, Description: "Constraint name", Type: "string"},
			},
			Response: ResponseSpec{Description: "Affected resources", SchemaRef: "ConstraintAffectedResponse"},
			handler:  s.handlePolicyConstraintAffected,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1/telemetry/status",
			Summary:     "Opt-in telemetry status",
			Description: "Reports whether anonymous usage telemetry is enabled, where it sends, and the last/next send times. v1-only.",
			Response:    ResponseSpec{Description: "Telemetry status", SchemaRef: "TelemetryStatusResponse"},
			handler:     s.handleTelemetryStatus,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1/telemetry/preview",
			Summary:     "Preview the next telemetry payload",
			Description: "Returns the exact, anonymous payload the next report would send — the transparency contract. Works whether or not telemetry is enabled, so a user can audit it before opting in. v1-only.",
			Response:    ResponseSpec{Description: "Telemetry payload", SchemaRef: "TelemetryPayload"},
			handler:     s.handleTelemetryPreview,
		},

		// Multi-cluster federation (P3-T22). v1-only — v1alpha1 is
		// frozen, and federation is the v1.3 net-new surface. Routes
		// without the /api/v1alpha1/ prefix register exactly once
		// (versionedPattern returns them unchanged).
		{
			Method:      "GET",
			Pattern:     "/api/v1/federation/clusters",
			Summary:     "List attached member clusters",
			Description: "Returns the set of clusters the multicluster.Manager is currently driving. Mode='single' with an empty cluster list means multicluster is disabled on this server.",
			Response:    ResponseSpec{Description: "Attached cluster list", SchemaRef: "FederationClustersResponse"},
			handler:     s.handleFederationClusters,
		},
		{
			Method:      "GET",
			Pattern:     "/api/v1/federation/graph",
			Summary:     "Federated graph across multiple clusters",
			Description: "Returns either a flat union of resources and intra-cluster edges across the named member clusters (level=resource, the default), or one Node per cluster with a resource count and a top-N kind summary (level=cluster, small payload). Every node carries its ClusterID so the UI can group / colour by cluster. 503 when multicluster is not enabled.",
			QueryParams: []ParamSpec{
				{Name: "cluster", Required: true, Description: "Comma-separated or repeated cluster names; every name must be attached.", Type: "string"},
				{Name: "level", Description: "View zoom; defaults to 'resource'. 'cluster' returns one summary Node per attached cluster.", Type: "string", Enum: []string{"resource", "cluster"}},
			},
			Response: ResponseSpec{Description: "Federated graph view", SchemaRef: "FederatedView"},
			handler:  s.handleFederationGraph,
		},
	}
}
