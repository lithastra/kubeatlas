// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
)

// History shares the existing two-request analysis admission budget. Workers
// that ignore cancellation retain their slot; only the handler writes HTTP.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request, diff bool) {
	ctx, cancel := context.WithTimeout(r.Context(), graph.HistoryReadTimeout)
	defer cancel()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Authorization")
	select {
	case s.impactSem <- struct{}{}:
	default:
		writeImpactReply(w, ctx, impactFailure(http.StatusTooManyRequests, CodeTooManyRequests, "analysis capacity is busy"))
		return
	}
	var remaining atomic.Int32
	remaining.Store(2)
	finish := func() {
		if remaining.Add(-1) == 0 {
			<-s.impactSem
		}
	}
	defer finish()
	result := make(chan impactReply, 1)
	go func() {
		reply := impactFailure(http.StatusInternalServerError, CodeInternal, "history read failed")
		defer func() {
			if recover() != nil {
				reply = impactFailure(http.StatusInternalServerError, CodeInternal, "history read failed")
			}
			finish()
			result <- reply
		}()
		reply = s.computeHistory(r.WithContext(ctx), diff)
	}()
	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.Canceled) {
			writeImpactReply(w, ctx, historyReadFailure(ctx.Err()))
		}
	case reply := <-result:
		if err := ctx.Err(); err != nil {
			if !errors.Is(err, context.Canceled) {
				writeImpactReply(w, ctx, historyReadFailure(err))
			}
			return
		}
		writeImpactReply(w, ctx, reply)
	}
}

func (s *Server) historyScope(r *http.Request) (string, impactReply) {
	allow, status := s.clusterVisibility(r)
	if status != 0 {
		if status == http.StatusUnauthorized {
			return "", impactFailure(status, "unauthorized", "a bearer token is required for cluster access")
		}
		return "", impactFailure(http.StatusForbidden, "forbidden", "not authorized for the requested cluster")
	}
	// The alpha surface gains no selector or response fields. Its unscoped
	// aliases cannot bypass member authorization, even when RBAC is disabled.
	if strings.HasPrefix(r.URL.Path, apiPrefixV1Alpha1) && (s.clusterLister != nil || allow != nil) {
		return "", impactFailure(http.StatusServiceUnavailable, CodeUnavailable, "unscoped history is unavailable; use the v1 history API with an explicit cluster")
	}
	values, present := r.URL.Query()["cluster"]
	cluster := ""
	if (apiVersionFor(r) == APIVersionV1 || r.URL.Path == "/api/_internal/snapshot/trigger") && present {
		if len(values) != 1 {
			return "", impactFailure(http.StatusBadRequest, CodeInvalidArgument, "select exactly one cluster")
		}
		cluster = values[0]
	}
	if s.clusterLister != nil {
		if !validImpactSegment(cluster, false) || strings.Contains(cluster, ",") {
			return "", impactFailure(http.StatusBadRequest, CodeInvalidArgument, "select exactly one cluster")
		}
	} else if cluster != "" {
		return "", impactFailure(http.StatusBadRequest, CodeInvalidArgument, "named cluster requires federation")
	}
	if allow != nil {
		if _, ok := allow[cluster]; !ok {
			return "", impactFailure(http.StatusForbidden, "forbidden", "not authorized for the requested cluster")
		}
	}
	if err := r.Context().Err(); err != nil {
		return "", historyReadFailure(err)
	}
	if s.clusterLister != nil {
		found := false
		for _, name := range s.clusterLister.ListClusters() {
			if name == cluster {
				found = true
				break
			}
		}
		if !found {
			return "", impactFailure(http.StatusNotFound, CodeNotFound, "cluster not found")
		}
	}
	return cluster, impactReply{}
}

func (s *Server) computeHistory(r *http.Request, diff bool) impactReply {
	cluster, failure := s.historyScope(r)
	if failure.status != 0 {
		return failure
	}
	ctx := r.Context()
	if err := ctx.Err(); err != nil {
		return historyReadFailure(err)
	}
	var response any
	if !diff {
		reader, ok := s.store.(graph.HistoryMarkerReader)
		if !ok {
			return historyReadFailure(errHistoryUnsupported)
		}
		markers, err := reader.ReadHistoryMarkers(ctx, cluster)
		if err != nil {
			return historyReadFailure(err)
		}
		// Explicit capability wrappers can still be faulty. Validate returned scope
		// and budgets before returning metadata or derived counts.
		b, err := graph.NewHistoryMarkerBuilder(cluster)
		if err != nil {
			return historyReadFailure(err)
		}
		for _, m := range markers {
			if err := b.Add(ctx, m); err != nil {
				return historyReadFailure(err)
			}
		}
		response = SnapshotListResponse{Snapshots: b.Markers, Count: len(b.Markers)}
	} else {
		now := time.Now()
		q := r.URL.Query()
		if strings.TrimSpace(q.Get("from")) == "" {
			return impactFailure(http.StatusBadRequest, CodeInvalidArgument, "from is required (e.g. from=5m)")
		}
		from, err := parseTimeParam(q.Get("from"), now)
		if err != nil {
			return impactFailure(http.StatusBadRequest, CodeInvalidArgument, "invalid history time window")
		}
		to, err := parseTimeParam(orDefault(q.Get("to"), "now"), now)
		if err != nil || !from.Before(to) || (s.snapshotRetention > 0 && to.Sub(from) > s.snapshotRetention) {
			return impactFailure(http.StatusBadRequest, CodeInvalidArgument, "invalid history time window or retention limit")
		}
		query := graph.HistoryEventQuery{ClusterID: cluster, Namespace: q.Get("namespace"), From: from, To: to, Limit: graph.MaxHistoryEvents, MaxBytes: graph.MaxHistoryBytes}
		query, err = query.Normalize()
		if err != nil {
			return impactFailure(http.StatusBadRequest, CodeInvalidArgument, "invalid history query")
		}
		reader, ok := s.store.(graph.HistoryEventReader)
		if !ok {
			return historyReadFailure(errHistoryUnsupported)
		}
		page, err := reader.ReadHistoryEvents(ctx, query)
		if err != nil {
			return historyReadFailure(err)
		}
		if page.Truncated || len(page.Events) > query.Limit {
			return historyReadFailure(graph.ErrHistoryEventLimit)
		}
		b, err := graph.NewHistoryEventPageBuilder(query)
		if err != nil {
			return historyReadFailure(err)
		}
		for i, e := range page.Events {
			if i > 0 {
				previous := page.Events[i-1]
				if previous.Timestamp.After(e.Timestamp) || (previous.Timestamp.Equal(e.Timestamp) && previous.ID >= e.ID) {
					return historyReadFailure(graph.ErrHistoryEventScope)
				}
			}
			if err := b.Add(ctx, e); err != nil {
				return historyReadFailure(err)
			}
		}
		response = analysis.DiffHistoryEvents(from, to, b.Page.Events)
	}
	if err := ctx.Err(); err != nil {
		return historyReadFailure(err)
	}
	body, err := json.Marshal(response)
	if err != nil {
		return historyReadFailure(err)
	}
	if len(body) > graph.MaxHistoryBytes {
		return historyReadFailure(graph.ErrHistoryEventLimit)
	}
	return impactReply{status: http.StatusOK, body: body}
}

var errHistoryUnsupported = errors.New("scoped history capability unavailable")

func historyReadFailure(err error) impactReply {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return impactFailure(http.StatusGatewayTimeout, "deadline_exceeded", "history read exceeded its request deadline")
	case errors.Is(err, graph.ErrHistoryEventLimit):
		return impactFailure(http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "history exceeds a configured size limit")
	case errors.Is(err, errHistoryUnsupported):
		return impactFailure(http.StatusServiceUnavailable, CodeUnavailable, "scoped history is unavailable")
	default:
		return impactFailure(http.StatusInternalServerError, CodeInternal, "history read failed")
	}
}
