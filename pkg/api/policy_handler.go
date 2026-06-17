// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// gatekeeperConstraintGroupPrefix identifies a Gatekeeper Constraint by
// its API group; kept in sync with the extractor's gate.
const gatekeeperConstraintGroupPrefix = "constraints.gatekeeper.sh/"

// PolicyConstraint summarises one admission-policy constraint for the
// policy list view.
type PolicyConstraint struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Engine     string `json:"engine"`
	Violations int    `json:"violations"`
}

// AffectedResource is one resource a constraint enforces, plus whether
// it currently violates and the controller's message.
type AffectedResource struct {
	Resource graph.Resource `json:"resource"`
	Violated bool           `json:"violated"`
	Message  string         `json:"message,omitempty"`
}

// ConstraintAffectedResponse is the body of
// /api/v1/policy/constraints/{name}/affected.
type ConstraintAffectedResponse struct {
	Constraint string             `json:"constraint"`
	Resources  []AffectedResource `json:"resources"`
	Count      int                `json:"count"`
}

// handlePolicyConstraints serves GET /api/v1/policy/constraints — every
// Gatekeeper Constraint with its live violation count, read from the
// Constraint's status (KubeAtlas never re-evaluates the policy).
//
// Optional query param engine restricts the engine; only "gatekeeper"
// is implemented today (Kyverno joins the same route later). The body
// is a JSON array, sorted by name for stable diffs.
func (s *Server) handlePolicyConstraints(w http.ResponseWriter, r *http.Request) {
	engine := r.URL.Query().Get("engine")
	if engine != "" && engine != "gatekeeper" && engine != "kyverno" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "unknown engine; supported: gatekeeper, kyverno")
		return
	}

	snap, err := s.store.Snapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}

	out := make([]PolicyConstraint, 0)
	for _, res := range snap.Resources {
		eng, ok := policyEngine(res)
		if !ok || (engine != "" && engine != eng) {
			continue
		}
		out = append(out, PolicyConstraint{
			Name:       res.Name,
			Kind:       res.Kind,
			Engine:     eng,
			Violations: s.policyViolations(r.Context(), res, eng),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Engine < out[j].Engine
	})
	writeJSON(w, http.StatusOK, out)
}

// handlePolicyConstraintAffected serves
// GET /api/v1/policy/constraints/{name}/affected — the resources the
// named constraint enforces, derived from its ENFORCES edges, each
// flagged with the violation status carried on the edge.
func (s *Server) handlePolicyConstraintAffected(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "constraint name is required")
		return
	}

	snap, err := s.store.Snapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}

	var constraintID string
	for _, res := range snap.Resources {
		if _, ok := policyEngine(res); ok && res.Name == name {
			constraintID = res.ID()
			break
		}
	}
	if constraintID == "" {
		writeError(w, http.StatusNotFound, CodeNotFound, "no constraint named "+name)
		return
	}

	edges, err := s.store.ListEdges(r.Context(), constraintID, graph.DirectionOutgoing)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}

	resources := make([]AffectedResource, 0)
	for _, e := range edges {
		if e.Type != graph.EdgeTypeEnforces {
			continue
		}
		res, err := s.store.GetResource(r.Context(), e.To)
		if err != nil {
			// Dangling target — the matched resource was deleted but the
			// edge hasn't been swept yet. Skip rather than 500.
			continue
		}
		// Gatekeeper tags violations as violated=true + a message;
		// Kyverno tags the PolicyReport result (fail = violating).
		resources = append(resources, AffectedResource{
			Resource: res,
			Violated: e.Attributes["violated"] == "true" || e.Attributes["result"] == "fail",
			Message:  e.Attributes["violation_message"],
		})
	}
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].Resource.ID() < resources[j].Resource.ID()
	})

	writeJSON(w, http.StatusOK, ConstraintAffectedResponse{
		Constraint: name,
		Resources:  resources,
		Count:      len(resources),
	})
}

// kyvernoGroupPrefix gates a resource as a Kyverno policy.
const kyvernoGroupPrefix = "kyverno.io/"

func isGatekeeperConstraint(r graph.Resource) bool {
	return strings.HasPrefix(r.GroupVersion, gatekeeperConstraintGroupPrefix)
}

// policyEngine classifies a resource as a policy object and names its
// engine, or returns ok=false for everything else.
func policyEngine(r graph.Resource) (string, bool) {
	switch {
	case isGatekeeperConstraint(r):
		return "gatekeeper", true
	case strings.HasPrefix(r.GroupVersion, kyvernoGroupPrefix) && (r.Kind == "ClusterPolicy" || r.Kind == "Policy"):
		return "kyverno", true
	default:
		return "", false
	}
}

// policyViolations counts a policy's current violations. Gatekeeper
// reads the Constraint's status; Kyverno counts failing ENFORCES edges
// (the PolicyReport result the extractor stamped on each edge).
func (s *Server) policyViolations(ctx context.Context, r graph.Resource, engine string) int {
	if engine == "gatekeeper" {
		return violationCount(r.Raw)
	}
	edges, err := s.store.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range edges {
		if e.Type == graph.EdgeTypeEnforces && e.Attributes["result"] == "fail" {
			n++
		}
	}
	return n
}

// violationCount reads len(status.violations) off a Constraint's raw
// object. Missing status / violations yields zero.
func violationCount(raw map[string]any) int {
	status, _ := raw["status"].(map[string]any)
	v, _ := status["violations"].([]any)
	return len(v)
}
