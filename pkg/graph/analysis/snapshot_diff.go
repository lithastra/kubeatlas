// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis

import (
	"context"
	"sort"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// DiffEntry identifies one resource that changed inside a diff
// window. It is metadata only — namespace / kind / name / uid plus
// the last event observed for that resource. The full resource
// payload is deliberately NOT included: the diff is a summary, and
// a caller who needs the object fetches it from
// /api/v1/resources/{...} (F-111 anti-pattern: the diff endpoint
// must not ship full resource data).
type DiffEntry struct {
	Namespace string          `json:"namespace"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	UID       string          `json:"uid,omitempty"`
	EventType graph.EventType `json:"eventType"`
	Timestamp time.Time       `json:"ts"`
}

// DiffResult is the outcome of DiffWindow: which resources were
// added, removed, or modified between From and To. Each slice is
// sorted by (namespace, kind, name) for stable output and is
// non-nil even when empty.
type DiffResult struct {
	From     time.Time   `json:"from"`
	To       time.Time   `json:"to"`
	Added    []DiffEntry `json:"added"`
	Removed  []DiffEntry `json:"removed"`
	Modified []DiffEntry `json:"modified"`
}

// DiffWindow computes the resource changes recorded in [from, to].
//
// It reads the resource_events stream for the window, groups events
// by resource identity, and classifies each resource from its first
// and last event in the window:
//
//	last == delete                 -> Removed
//	last != delete, first == add   -> Added
//	last != delete, first != add   -> Modified
//
// Rationale: the last event decides whether the resource still
// exists at To; the first event decides whether it already existed
// at From (a window that opens with an `add` means the resource
// appeared during the window). A resource added AND deleted inside
// the same window classifies as Removed — its final state is gone;
// this is a rare corner case and "removed" is the honest label for
// "not here anymore".
//
// An empty namespace diffs the whole cluster; a non-empty namespace
// scopes the query. The store's ListEvents already returns events
// oldest-first, so group[0] is the earliest and group[len-1] the
// latest.
func DiffWindow(ctx context.Context, store graph.GraphStore, from, to time.Time, namespace string) (DiffResult, error) {
	res := DiffResult{
		From:     from,
		To:       to,
		Added:    []DiffEntry{},
		Removed:  []DiffEntry{},
		Modified: []DiffEntry{},
	}

	events, err := store.ListEvents(ctx, namespace, from, to)
	if err != nil {
		return DiffResult{}, err
	}

	// Group events by resource identity. UID is the stable key; an
	// event with no UID (rare — e.g. a resource the apiserver never
	// assigned one) falls back to the ns/kind/name triple.
	groups := make(map[string][]graph.ResourceEvent)
	order := make([]string, 0)
	for _, e := range events {
		key := e.UID
		if key == "" {
			key = e.Namespace + "/" + e.Kind + "/" + e.Name
		}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], e)
	}

	for _, key := range order {
		g := groups[key]
		first := g[0].EventType
		last := g[len(g)-1]
		entry := diffEntryOf(last)
		switch {
		case last.EventType == graph.EventTypeDelete:
			res.Removed = append(res.Removed, entry)
		case first == graph.EventTypeAdd:
			res.Added = append(res.Added, entry)
		default:
			res.Modified = append(res.Modified, entry)
		}
	}

	sortDiffEntries(res.Added)
	sortDiffEntries(res.Removed)
	sortDiffEntries(res.Modified)
	return res, nil
}

// diffEntryOf builds the metadata-only DiffEntry from a
// ResourceEvent, dropping the (possibly large) Data payload.
func diffEntryOf(e graph.ResourceEvent) DiffEntry {
	return DiffEntry{
		Namespace: e.Namespace,
		Kind:      e.Kind,
		Name:      e.Name,
		UID:       e.UID,
		EventType: e.EventType,
		Timestamp: e.Timestamp,
	}
}

// sortDiffEntries orders a bucket by (namespace, kind, name) so the
// diff output is byte-stable across successive calls.
func sortDiffEntries(es []DiffEntry) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].Namespace != es[j].Namespace {
			return es[i].Namespace < es[j].Namespace
		}
		if es[i].Kind != es[j].Kind {
			return es[i].Kind < es[j].Kind
		}
		return es[i].Name < es[j].Name
	})
}
