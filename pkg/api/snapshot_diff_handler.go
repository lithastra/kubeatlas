// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lithastra/kubeatlas/pkg/snapshot"
)

// handleSnapshotDiff serves GET /api/v1/snapshots/diff — the
// "what changed between then and now" endpoint.
//
// Query params:
//
//	from       required. "now", a duration ("5m"/"1h"/"7d", read as
//	           "ago"), or an RFC3339 timestamp.
//	to         optional, defaults to "now". Same formats as `from`.
//	namespace  optional. Empty diffs the whole cluster.
//
// The window must be non-empty (from < to) and no wider than the
// configured retention — the event stream beyond retention has
// been pruned, so a wider window would silently under-report.
func (s *Server) handleSnapshotDiff(w http.ResponseWriter, r *http.Request) {
	if !s.snapshotsEnabled {
		snapshotsUnavailable(w)
		return
	}
	s.handleHistory(w, r, true)
}

// parseTimeParam resolves a from/to query value into an absolute
// time, relative to now:
//
//	""  / "now"          -> now
//	"5m" / "1h" / "7d"   -> now minus that duration ("ago")
//	RFC3339 timestamp     -> that absolute instant
//
// The relative form reuses snapshot.ParseRetention so the day
// suffix ("7d") behaves the same here as in the Helm retention
// value.
func parseTimeParam(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "now" {
		return now, nil
	}
	if d, err := snapshot.ParseRetention(s); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf(
		"invalid time %q: want 'now', a duration like '5m'/'1h'/'7d', or an RFC3339 timestamp", s)
}

// orDefault returns v when non-empty, else def.
func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
