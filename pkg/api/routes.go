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
			Summary:     "Substring search across resources",
			Description: "Linear case-insensitive scan over kind / name / namespace / labels. Phase 2 (v1.0) replaces this with an inverted index.",
			QueryParams: []ParamSpec{
				{Name: "q", Required: true, Description: "Search term (case-insensitive)", Type: "string"},
				{Name: "limit", Description: "Max matches to return (default 50, max 200)", Type: "integer"},
			},
			Response: ResponseSpec{Description: "Search results", SchemaRef: "SearchResponse"},
			handler:  s.handleSearch,
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
	}
}
