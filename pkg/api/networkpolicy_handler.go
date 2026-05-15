// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"net/http"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// NetworkPolicySelectedResponse is the body of
// GET /api/v1alpha1/networkpolicy/{namespace}/{name}/selected.
//
// NetworkPolicy is the policy the query was rooted at; Selected is
// every Pod (or Pod-template-carrying workload) the policy's
// spec.podSelector matches, resolved from the SELECTS_NP edges the
// F-109 extractor persisted. Count is pre-computed so dashboards
// don't len() client-side.
type NetworkPolicySelectedResponse struct {
	NetworkPolicy graph.Resource   `json:"networkPolicy"`
	Selected      []graph.Resource `json:"selected"`
	Count         int              `json:"count"`
}

// NetworkPolicyAllowGraphResponse is the body of
// GET /api/v1alpha1/networkpolicy/{namespace}/{name}/allow-graph.
//
// AllowFrom / AllowTo are the resolved targets of the ALLOWS_FROM /
// ALLOWS_TO edges — declared ingress sources and egress
// destinations. Targets are Pods, workloads, or Namespaces; an
// edge whose target no longer exists in the store (the Pod was
// deleted after the policy was observed) is omitted rather than
// returned as a dangling stub.
type NetworkPolicyAllowGraphResponse struct {
	NetworkPolicy graph.Resource   `json:"networkPolicy"`
	AllowFrom     []graph.Resource `json:"allowFrom"`
	AllowTo       []graph.Resource `json:"allowTo"`
}

// handleNetworkPolicySelected serves
// GET /networkpolicy/{namespace}/{name}/selected.
func (s *Server) handleNetworkPolicySelected(w http.ResponseWriter, r *http.Request) {
	np, id, ok := s.lookupNetworkPolicy(w, r)
	if !ok {
		return
	}
	out, err := s.store.ListOutgoing(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	selected := s.resolveEdgeTargets(r, out, graph.EdgeTypeSelectsNP)
	writeJSON(w, http.StatusOK, NetworkPolicySelectedResponse{
		NetworkPolicy: np,
		Selected:      selected,
		Count:         len(selected),
	})
}

// handleNetworkPolicyAllowGraph serves
// GET /networkpolicy/{namespace}/{name}/allow-graph.
func (s *Server) handleNetworkPolicyAllowGraph(w http.ResponseWriter, r *http.Request) {
	np, id, ok := s.lookupNetworkPolicy(w, r)
	if !ok {
		return
	}
	out, err := s.store.ListOutgoing(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, NetworkPolicyAllowGraphResponse{
		NetworkPolicy: np,
		AllowFrom:     s.resolveEdgeTargets(r, out, graph.EdgeTypeAllowsFrom),
		AllowTo:       s.resolveEdgeTargets(r, out, graph.EdgeTypeAllowsTo),
	})
}

// lookupNetworkPolicy resolves the {namespace}/{name} path values
// into the NetworkPolicy resource. On a missing path value or a
// missing resource it writes the error response and returns
// ok=false; callers return immediately when ok is false.
func (s *Server) lookupNetworkPolicy(w http.ResponseWriter, r *http.Request) (graph.Resource, string, bool) {
	ns := r.PathValue("namespace")
	name := r.PathValue("name")
	if ns == "" || name == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"URL must be /networkpolicy/{namespace}/{name}/...")
		return graph.Resource{}, "", false
	}
	id := makeID(ns, "NetworkPolicy", name)
	np, err := s.store.GetResource(r.Context(), id)
	if err != nil {
		writeNotFoundOr500(w, err, id)
		return graph.Resource{}, "", false
	}
	return np, id, true
}

// resolveEdgeTargets filters edges to the given type and resolves
// each target ID into a Resource. Targets that no longer exist
// (dangling edges — the Pod was deleted after the NetworkPolicy
// was observed) are skipped, so the result is always a clean list
// of live resources. A non-NotFound store error aborts resolution
// of the rest and returns what was collected so far; callers treat
// the result as best-effort. The returned slice is non-nil so JSON
// encodes [] rather than null.
func (s *Server) resolveEdgeTargets(r *http.Request, edges []graph.Edge, t graph.EdgeType) []graph.Resource {
	out := make([]graph.Resource, 0)
	for _, e := range edges {
		if e.Type != t {
			continue
		}
		res, err := s.store.GetResource(r.Context(), e.To)
		if err != nil {
			var nf graph.ErrNotFound
			if errors.As(err, &nf) {
				continue // dangling edge — target deleted; skip
			}
			return out // infrastructure error — return best-effort
		}
		out = append(out, res)
	}
	return out
}
