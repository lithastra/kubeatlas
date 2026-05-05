package api

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

// ResourceDetailResponse is the body of GET /resources/{ns}/{kind}/{name}.
// Resource is the K8s metadata + Raw spec from the store; Incoming and
// Outgoing are the edges incident on this resource.
type ResourceDetailResponse struct {
	Resource graph.Resource `json:"resource"`
	Incoming []graph.Edge   `json:"incoming"`
	Outgoing []graph.Edge   `json:"outgoing"`
}

// SearchResponse is the body of GET /search?q=...
//
// Truncated is true when the cap was hit — the client should narrow
// its query rather than ask for a higher limit.
type SearchResponse struct {
	Matches   []graph.Resource `json:"matches"`
	Total     int              `json:"total"`
	Truncated bool             `json:"truncated"`
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

// handleResource serves GET /resources/{namespace}/{kind}/{name}.
// The detail bundles the resource itself with its incoming + outgoing
// edges so the UI's resource page can render in one round-trip.
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
// Phase 1's search is a linear case-insensitive substring scan over
// every resource's kind / name / namespace / label values. Phase 2
// (v1.0) replaces this with an inverted index — until then, this
// scales fine for typical Phase 1 cluster sizes but the spec keeps a
// per-request cap so a 50K-resource cluster doesn't return everything.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "q is required and must not be empty")
		return
	}
	limit := defaultSearchLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "limit must be a positive integer")
			return
		}
		if n > maxSearchLimit {
			n = maxSearchLimit
		}
		limit = n
	}
	snap, err := s.store.Snapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	needle := strings.ToLower(q)
	matches := make([]graph.Resource, 0, limit)
	total := 0
	for _, res := range snap.Resources {
		if !resourceMatches(res, needle) {
			continue
		}
		total++
		if len(matches) < limit {
			matches = append(matches, res)
		}
	}
	// Stable order so two consecutive calls with the same query and
	// store contents return the same prefix.
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID() < matches[j].ID() })
	writeJSON(w, http.StatusOK, SearchResponse{
		Matches:   matches,
		Total:     total,
		Truncated: total > len(matches),
	})
}

// handleMetrics serves the Prometheus exposition.
func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	writePrometheus(w, s.readiness, s.metrics)
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

// resourceMatches returns true if needle (already lower-cased) appears
// as a substring of any of: Kind, Name, Namespace, or any label value
// or key.
func resourceMatches(r graph.Resource, needle string) bool {
	if strings.Contains(strings.ToLower(r.Kind), needle) ||
		strings.Contains(strings.ToLower(r.Name), needle) ||
		strings.Contains(strings.ToLower(r.Namespace), needle) {
		return true
	}
	for k, v := range r.Labels {
		if strings.Contains(strings.ToLower(k), needle) || strings.Contains(strings.ToLower(v), needle) {
			return true
		}
	}
	return false
}
