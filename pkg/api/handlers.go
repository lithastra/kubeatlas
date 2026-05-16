package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

// ResourceDetailResponse is the v1alpha1 body of
// GET /resources/{ns}/{kind}/{name}. Resource is the K8s metadata +
// Raw spec from the store; Incoming and Outgoing are the edges
// incident on this resource.
//
// This shape is FROZEN. api-compat-check enforces that no field
// is removed or renamed — the v1 surface gets enrichment fields
// via the sibling ResourceDetailResponseV1 type below.
type ResourceDetailResponse struct {
	Resource graph.Resource `json:"resource"`
	Incoming []graph.Edge   `json:"incoming"`
	Outgoing []graph.Edge   `json:"outgoing"`
}

// ResourceDetailResponseV1 is the GA superset returned by the
// v1 endpoint. The first three fields are byte-identical to
// ResourceDetailResponse so v1alpha1 consumers that drift to v1
// see exactly the same data; the rest are graph-analysis
// enrichments (P2-T15 / T17 / T18) folded into the resource
// detail bundle so the UI can render badges without round-trips.
type ResourceDetailResponseV1 struct {
	Resource         graph.Resource `json:"resource"`
	Incoming         []graph.Edge   `json:"incoming"`
	Outgoing         []graph.Edge   `json:"outgoing"`
	BlastRadiusCount int            `json:"blastRadiusCount"`
	IsOrphan         bool           `json:"isOrphan"`
	InCycle          bool           `json:"inCycle"`
}

// SearchResponse is the body of GET /search?q=...
//
// Truncated is true when the cap was hit — the client should narrow
// its query rather than ask for a higher limit. Warning is set when
// the search ran as an unindexed linear scan (a Tier 1 store); it is
// omitted entirely on the indexed Tier 2 path.
type SearchResponse struct {
	Matches   []graph.Resource `json:"matches"`
	Total     int              `json:"total"`
	Truncated bool             `json:"truncated"`
	Warning   string           `json:"warning,omitempty"`
}

const (
	defaultSearchLimit = 50
	maxSearchLimit     = 200

	// clusterNamespaceSentinel is what the URL uses for cluster-scoped
	// resources, since "" can't appear in a path segment. The handler
	// translates "_" back to "" before talking to the store.
	clusterNamespaceSentinel = "_"
)

// handleGraph dispatches GET /api/v1alpha1/graph?level=...
//
// Common params:
//
//	level=cluster|namespace|workload|resource (required)
//	namespace=<ns>            (required for namespace/workload/resource)
//	kind=<kind>               (required for workload/resource)
//	name=<name>               (required for workload/resource)
func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	levelStr := q.Get("level")
	if levelStr == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "level is required")
		return
	}
	level := aggregator.Level(levelStr)
	agg, ok := s.aggs.Get(level)
	if !ok {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "unknown level: "+levelStr)
		return
	}
	scope := aggregator.Scope{
		Namespace: q.Get("namespace"),
		Kind:      q.Get("kind"),
		Name:      q.Get("name"),
	}
	view, err := agg.Aggregate(r.Context(), s.store, scope)
	if err != nil {
		var nf graph.ErrNotFound
		if errors.As(err, &nf) {
			writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
			return
		}
		// Aggregators return errors only for invalid scope or store
		// failures; the former is a client bug, the latter is internal.
		// We can't easily tell them apart without a typed error from
		// the aggregator; treat scope-shape mismatches as 400 by
		// matching common phrasing.
		if strings.Contains(err.Error(), "requires") || strings.Contains(err.Error(), "required") {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleResource serves GET /resources/{namespace}/{kind}/{name}
// on both API versions. The store query path is identical for
// both; only the response DTO differs (v1 adds graph-analysis
// enrichment fields).
func (s *Server) handleResource(w http.ResponseWriter, r *http.Request) {
	ns, kind, name, ok := pathParts(r)
	if !ok {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "URL must be /resources/{namespace}/{kind}/{name}")
		return
	}
	id := makeID(ns, kind, name)
	res, err := s.store.GetResource(r.Context(), id)
	if err != nil {
		writeNotFoundOr500(w, err, id)
		return
	}
	in, err := s.store.ListIncoming(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	out, err := s.store.ListOutgoing(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	if apiVersionFor(r) == APIVersionV1 {
		writeJSON(w, http.StatusOK, s.buildResourceDetailV1(r, res, in, out))
		return
	}
	writeJSON(w, http.StatusOK, ResourceDetailResponse{
		Resource: res,
		Incoming: in,
		Outgoing: out,
	})
}

// handleIncoming serves GET /resources/{ns}/{kind}/{name}/incoming.
func (s *Server) handleIncoming(w http.ResponseWriter, r *http.Request) {
	ns, kind, name, ok := pathParts(r)
	if !ok {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "URL must be /resources/{namespace}/{kind}/{name}/incoming")
		return
	}
	id := makeID(ns, kind, name)
	if _, err := s.store.GetResource(r.Context(), id); err != nil {
		writeNotFoundOr500(w, err, id)
		return
	}
	edges, err := s.store.ListIncoming(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	if edges == nil {
		edges = []graph.Edge{}
	}
	writeJSON(w, http.StatusOK, edges)
}

// handleOutgoing serves GET /resources/{ns}/{kind}/{name}/outgoing.
func (s *Server) handleOutgoing(w http.ResponseWriter, r *http.Request) {
	ns, kind, name, ok := pathParts(r)
	if !ok {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "URL must be /resources/{namespace}/{kind}/{name}/outgoing")
		return
	}
	id := makeID(ns, kind, name)
	if _, err := s.store.GetResource(r.Context(), id); err != nil {
		writeNotFoundOr500(w, err, id)
		return
	}
	edges, err := s.store.ListOutgoing(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	if edges == nil {
		edges = []graph.Edge{}
	}
	writeJSON(w, http.StatusOK, edges)
}

// handleSearch serves GET /search?q=&limit=.
//
// F-113 (P3-T8): the query is pushed into the store. On Tier 2 it
// runs as one GIN-indexed tsvector match; on Tier 1 it is a linear
// scan and the response carries a Warning saying so. The Phase 1
// path — store.Snapshot + a Go-side scan — is gone: it materialised
// every resource into the API process and OOM-killed the pod past
// ~5K resources (the same defect P3-T0a removed from the views).
//
// q accepts free-text terms plus "kind:" / "namespace:" (or "ns:")
// filter tokens; see parseSearchQuery. limit is capped at
// maxSearchLimit so a huge cluster cannot return everything.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("q"))
	if raw == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "q is required and must not be empty")
		return
	}
	limit := defaultSearchLimit
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		n, err := strconv.Atoi(rawLimit)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "limit must be a positive integer")
			return
		}
		if n > maxSearchLimit {
			n = maxSearchLimit
		}
		limit = n
	}

	query := parseSearchQuery(raw)
	query.Limit = limit
	if query.Text == "" && query.Kind == "" && query.Namespace == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"q must contain a search term or a kind:/namespace: filter")
		return
	}

	result, err := s.store.Search(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	matches := result.Matches
	if matches == nil {
		matches = []graph.Resource{}
	}
	resp := SearchResponse{
		Matches:   matches,
		Total:     result.Total,
		Truncated: result.Total > len(matches),
	}
	if result.LinearScan {
		resp.Warning = "search ran as a linear scan on a Tier 1 (in-memory) store " +
			"and may be slow on large clusters; enable Tier 2 (PostgreSQL) for indexed search"
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseSearchQuery splits a raw q value into the F-113 query model:
// "kind:" / "namespace:" / "ns:" tokens become exact-match filters,
// every other token is free text. The query model stays this small
// for v1.1 — no boolean DSL, no quoting (ADR 0011). A repeated
// filter token keeps the last value; a token with an unrecognised
// "prefix:" is treated as plain free text.
func parseSearchQuery(raw string) graph.SearchQuery {
	var (
		q     graph.SearchQuery
		terms []string
	)
	for _, tok := range strings.Fields(raw) {
		key, val, isFilter := strings.Cut(tok, ":")
		if !isFilter || val == "" {
			terms = append(terms, tok)
			continue
		}
		switch strings.ToLower(key) {
		case "kind":
			q.Kind = val
		case "namespace", "ns":
			q.Namespace = val
		default:
			terms = append(terms, tok)
		}
	}
	q.Text = strings.Join(terms, " ")
	return q
}

// handleMetrics serves the Prometheus exposition.
func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	writePrometheus(w, s.readiness, s.metrics, s.regoMetrics, s.regoModuleCount,
		s.snapshotMetrics, s.snapshotQueueDepth)
}

// pathParts pulls (namespace, kind, name) out of r.PathValue calls. The
// route patterns in registerRoutes use {namespace}, {kind}, {name}.
// All three must be non-empty; namespace == "_" maps to the empty
// string for cluster-scoped resources.
func pathParts(r *http.Request) (ns, kind, name string, ok bool) {
	ns = r.PathValue("namespace")
	kind = r.PathValue("kind")
	name = r.PathValue("name")
	if kind == "" || name == "" {
		return "", "", "", false
	}
	if ns == clusterNamespaceSentinel {
		ns = ""
	}
	return ns, kind, name, true
}

func makeID(ns, kind, name string) string {
	return ns + "/" + kind + "/" + name
}

func writeNotFoundOr500(w http.ResponseWriter, err error, id string) {
	var nf graph.ErrNotFound
	if errors.As(err, &nf) {
		writeError(w, http.StatusNotFound, CodeNotFound, "resource not found: "+id)
		return
	}
	writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
}
