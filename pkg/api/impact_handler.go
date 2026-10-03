// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/impact"
	"github.com/lithastra/kubeatlas/pkg/version"
)

const (
	impactPath           = "/api/v1/impact/{namespace}/{kind}/{name}"
	impactRequestTimeout = 5 * time.Second
	impactResponseBytes  = impact.MaxResponseBytes
	impactConcurrency    = 2
	impactSharingWarning = impact.SharingWarning
)

type ImpactScope = impact.Scope

// ImpactResponse is the v1-only envelope; its analysis facets share one encoded
// byte cap. Root namespace identifies the root, not a traversal display filter.
type ImpactResponse = impact.Response

type impactReply struct {
	status int
	body   []byte
}

func impactFailure(status int, code, message string) impactReply {
	body, _ := json.Marshal(ErrorResponse{Code: code, Error: message})
	return impactReply{status: status, body: body}
}

func impactDeadlineReply() impactReply {
	return impactFailure(http.StatusGatewayTimeout, "deadline_exceeded", "impact analysis exceeded its request deadline")
}

func (s *Server) handleImpact(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), impactRequestTimeout)
	defer cancel()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Authorization")
	if err := ctx.Err(); err != nil {
		if !errors.Is(err, context.Canceled) {
			writeImpactReply(w, ctx, impactDeadlineReply())
		}
		return
	}
	select {
	case s.impactSem <- struct{}{}:
	default:
		writeImpactReply(w, ctx, impactFailure(http.StatusTooManyRequests, CodeTooManyRequests, "impact analysis capacity is busy"))
		return
	}
	finished := make(chan impactReply, 1)
	var outstanding atomic.Int32
	outstanding.Store(2) // Worker computation and handler response writing.
	finishPart := func() {
		if outstanding.Add(-1) == 0 {
			<-s.impactSem
		}
	}
	defer finishPart()
	// Only this handler writes the response. A cancelled/late worker cannot
	// append JSON or convert a timeout to success. Admission stays occupied
	// until BOTH work and response writing finish, bounding slow writers and
	// even a faulty provider that ignores its required context cancellation.
	go func() {
		reply := impactFailure(http.StatusInternalServerError, CodeInternal, "impact analysis failed")
		defer func() {
			if recover() != nil {
				reply = impactFailure(http.StatusInternalServerError, CodeInternal, "impact analysis failed")
			}
			// Finish work BEFORE publishing the reply, so a completed response
			// releases admission synchronously, without another worker wakeup.
			finishPart()
			finished <- reply
		}()
		reply = s.computeImpact(r.WithContext(ctx))
	}()
	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.Canceled) {
			writeImpactReply(w, ctx, impactDeadlineReply())
		}
	case reply := <-finished:
		if err := ctx.Err(); err != nil {
			if !errors.Is(err, context.Canceled) {
				writeImpactReply(w, ctx, impactDeadlineReply())
			}
			return
		}
		writeImpactReply(w, ctx, reply)
	}
}

func writeImpactReply(w http.ResponseWriter, ctx context.Context, reply impactReply) {
	// Bind real HTTP socket writes to the same deadline; in-memory test writers
	// may not support it. Flush under the deadline before resetting keep-alive
	// state. A disconnected/expired socket may not deliver even an error reply.
	controller := http.NewResponseController(w)
	if deadline, ok := ctx.Deadline(); ok {
		_ = controller.SetWriteDeadline(deadline)
		defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if reply.status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	w.WriteHeader(reply.status)
	if _, err := w.Write(reply.body); err == nil {
		_ = controller.Flush()
	}
}

func (s *Server) computeImpact(r *http.Request) impactReply {
	ctx := r.Context()
	root, opts, err := parseImpactRequest(r, s.clusterLister != nil)
	if err != nil {
		return impactFailure(http.StatusBadRequest, CodeInvalidArgument, "invalid impact query or resource identity")
	}
	allow, status := s.clusterVisibility(r)
	if status != 0 {
		if status == http.StatusUnauthorized {
			return impactFailure(status, "unauthorized", "a bearer token is required for cluster access")
		}
		return impactFailure(http.StatusForbidden, "forbidden", "not authorized for the requested cluster")
	}
	if allow != nil {
		if _, ok := allow[opts.ClusterID]; !ok {
			return impactFailure(http.StatusForbidden, "forbidden", "not authorized for the requested cluster")
		}
	}
	if err := ctx.Err(); err != nil {
		return impactDeadlineReply()
	}
	// Check authorization before attachment/existence, not after obtaining an
	// unfiltered cluster list. Unknown and missing roots have closed error text.
	mode := "single"
	if s.clusterLister != nil {
		mode = "federated"
		found := false
		for _, cluster := range s.clusterLister.ListClusters() {
			if cluster == opts.ClusterID {
				found = true
				break
			}
		}
		if !found {
			return impactFailure(http.StatusNotFound, CodeNotFound, "resource or cluster not found")
		}
	}
	if err := ctx.Err(); err != nil {
		return impactDeadlineReply()
	}
	result, err := analysis.AnalyzeImpactWithEvidence(ctx, s.store, root.ID(), opts,
		graph.ImpactSnapshotOptions{ClusterID: opts.ClusterID}, s.impactEvidence)
	if err != nil {
		return impactAnalysisFailure(err)
	}
	response := ImpactResponse{
		SchemaVersion: "1", KubeAtlasVersion: version.Version, GeneratedAt: time.Now().UTC(),
		Scope:    ImpactScope{ClusterID: opts.ClusterID, Mode: mode, NamespaceScope: "all_namespaces_in_selected_cluster"},
		Analysis: result, SharingWarning: impactSharingWarning,
	}
	body, err := encodeImpactResponse(ctx, response, impactResponseBytes)
	if err != nil {
		return impactAnalysisFailure(err)
	}
	return impactReply{status: http.StatusOK, body: body}
}

func impactAnalysisFailure(err error) impactReply {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return impactDeadlineReply()
	case errors.Is(err, analysis.ErrImpactRootNotFound):
		return impactFailure(http.StatusNotFound, CodeNotFound, "resource or cluster not found")
	case errors.Is(err, analysis.ErrImpactUIDMismatch), errors.Is(err, analysis.ErrImpactUIDUnknown):
		return impactFailure(http.StatusConflict, "conflict", "expected UID cannot be matched to the observed resource instance")
	case errors.Is(err, errImpactResponseLimit), errors.Is(err, graph.ErrImpactSnapshotLimit), errors.Is(err, analysis.ErrImpactReferenceLimit), errors.Is(err, analysis.ErrImpactAuthorizationLimit):
		return impactFailure(http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "impact analysis exceeds a configured size limit")
	case errors.Is(err, analysis.ErrImpactSnapshotUnsupported), errors.Is(err, analysis.ErrImpactObservation), errors.Is(err, analysis.ErrImpactAvailability):
		return impactFailure(http.StatusServiceUnavailable, CodeUnavailable, "impact evidence is unavailable")
	default:
		// Never forward database/provider/panic text, resource IDs, or credentials.
		return impactFailure(http.StatusInternalServerError, CodeInternal, "impact analysis failed")
	}
}

func parseImpactRequest(r *http.Request, federated bool) (graph.Resource, analysis.ImpactOptions, error) {
	invalid := func() (graph.Resource, analysis.ImpactOptions, error) {
		return graph.Resource{}, analysis.ImpactOptions{}, analysis.ErrImpactInvalidOptions
	}
	if len(r.URL.RawQuery) > 8192 {
		return invalid()
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return invalid()
	}
	for key, values := range query {
		if len(values) != 1 || values[0] == "" {
			return invalid()
		}
		switch key {
		case "cluster", "relation", "max_depth", "limit", "expected_uid":
		default:
			return invalid()
		}
	}
	root := graph.Resource{ClusterID: query.Get("cluster"), Namespace: r.PathValue("namespace"), Kind: r.PathValue("kind"), Name: r.PathValue("name")}
	if !validImpactSegment(root.Namespace, false) || !validImpactSegment(root.Kind, false) || !validImpactSegment(root.Name, true) {
		return invalid()
	}
	if root.Namespace == "_" {
		root.Namespace = ""
	}
	if federated {
		if !validImpactSegment(root.ClusterID, false) || strings.Contains(root.ClusterID, ",") {
			return invalid()
		}
	} else if root.ClusterID != "" {
		return invalid()
	}
	opts := analysis.ImpactOptions{ClusterID: root.ClusterID, Relation: analysis.ImpactDependents,
		MaxDepth: analysis.DefaultImpactDepth, Limit: analysis.DefaultImpactLimit, ExpectedUID: query.Get("expected_uid")}
	if opts.ExpectedUID != "" && !validImpactSegment(opts.ExpectedUID, true) {
		return invalid()
	}
	if relation, present := query["relation"]; present {
		opts.Relation = analysis.ImpactRelation(relation[0])
		if opts.Relation != analysis.ImpactDependents && opts.Relation != analysis.ImpactDependencies {
			return invalid()
		}
	}
	for _, bound := range []struct {
		key    string
		target *int
		max    int
	}{{"max_depth", &opts.MaxDepth, analysis.MaxImpactDepth}, {"limit", &opts.Limit, analysis.MaxImpactLimit}} {
		if raw, present := query[bound.key]; present {
			// Only canonical positive decimal values. Explicit zero is invalid,
			// unlike the internal Go option's zero-means-default convention.
			n, err := strconv.Atoi(raw[0])
			if err != nil || n < 1 || n > bound.max || strconv.Itoa(n) != raw[0] {
				return invalid()
			}
			*bound.target = n
		}
	}
	return root, opts, nil
}

func validImpactSegment(value string, colon bool) bool {
	if value == "" || len(value) > 253 || !utf8.ValidString(value) || strings.ContainsAny(value, "/\\") || !colon && strings.Contains(value, ":") {
		return false
	}
	for _, ch := range value {
		if unicode.IsControl(ch) || unicode.IsSpace(ch) {
			return false
		}
	}
	return true
}
