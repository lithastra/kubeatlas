// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// RunHistoryEvents is the shared optional-capability contract. It intentionally
// makes no claim that the memory ring is durable or a queried window complete.
func RunHistoryEvents(t *testing.T, factory Factory) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 9, 30, 1, 0, 0, 0, time.UTC)
	query := graph.HistoryEventQuery{ClusterID: "prod", Namespace: "demo", From: base, To: base.Add(time.Hour)}
	newStore := func(t *testing.T) (graph.GraphStore, graph.HistoryEventReader) {
		t.Helper()
		s := factory(t)
		r, ok := s.(graph.HistoryEventReader)
		if !ok {
			t.Fatal("store must explicitly implement HistoryEventReader")
		}
		return s, r
	}
	appendEvent := func(t *testing.T, s graph.GraphStore, e graph.ResourceEvent) {
		t.Helper()
		if err := s.AppendEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	event := graph.ResourceEvent{Timestamp: base, ClusterID: "prod", Namespace: "demo", Kind: "ConfigMap",
		Name: "same", UID: "same-uid", EventType: graph.EventTypeUpdate, ResourceVersion: "1"}

	t.Run("empty is an owned non-nil page not coverage proof", func(t *testing.T) {
		_, r := newStore(t)
		got, err := r.ReadHistoryEvents(ctx, query)
		if err != nil || got.Events == nil || len(got.Events) != 0 || got.Truncated {
			t.Fatalf("empty page=%+v error=%v", got, err)
		}
	})
	t.Run("default row bound and oversized lookahead", func(t *testing.T) {
		s, r := newStore(t)
		for range graph.DefaultHistoryEvents {
			appendEvent(t, s, event)
		}
		page, err := r.ReadHistoryEvents(ctx, query)
		if err != nil || len(page.Events) != graph.DefaultHistoryEvents || page.Truncated {
			t.Fatal("exact default limit must not imply truncation")
		}
		e := event
		e.Name = strings.Repeat("lookahead", graph.DefaultHistoryBytes/8)
		appendEvent(t, s, e)
		page, err = r.ReadHistoryEvents(ctx, query)
		if err != nil || len(page.Events) != graph.DefaultHistoryEvents || !page.Truncated {
			t.Fatal("extra row must establish truncation without consuming metadata budget")
		}
	})
	t.Run("exact cluster equality including empty and SQL metacharacters", func(t *testing.T) {
		s, r := newStore(t)
		clusters := []string{"", "prod", "prod_", "prod%", "prodX", "prod' OR '1'='1"}
		for _, cluster := range clusters {
			e := event
			e.ClusterID = cluster
			appendEvent(t, s, e)
		}
		for _, cluster := range clusters {
			q := query
			q.ClusterID, q.Limit = cluster, 1
			got, err := r.ReadHistoryEvents(ctx, q)
			if err != nil || len(got.Events) != 1 || got.Truncated || got.Events[0].ClusterID != cluster {
				t.Fatalf("cluster %q: page=%+v error=%v", cluster, got, err)
			}
		}
		// Existing ListEvents remains an unscoped legacy method. Never use it
		// as a fallback for the selected-cluster capability.
		legacy, err := s.ListEvents(ctx, "demo", query.From, query.To)
		if err != nil || len(legacy) != len(clusters) {
			t.Fatalf("legacy method changed: count=%d error=%v", len(legacy), err)
		}
	})
	t.Run("scope and Secret filtering precede limits and byte budget", func(t *testing.T) {
		s, r := newStore(t)
		for _, variant := range []string{"cluster", "namespace", "before", "after", "Secret"} {
			e := event
			e.Name = strings.Repeat("hidden-canary", 2000)
			switch variant {
			case "cluster":
				e.ClusterID = "other"
			case "namespace":
				e.Namespace = "other"
			case "before":
				e.Timestamp = query.From.Add(-time.Second)
			case "after":
				e.Timestamp = query.To.Add(time.Second)
			case "Secret":
				e.Kind = "Secret"
			}
			appendEvent(t, s, e)
		}
		appendEvent(t, s, event)
		q := query
		q.Limit, q.MaxBytes = 1, 1024
		got, err := r.ReadHistoryEvents(ctx, q)
		if err != nil || len(got.Events) != 1 || got.Truncated || got.Events[0].Name != "same" {
			t.Fatalf("excluded rows changed page/budget: page=%+v error=%v", got, err)
		}
	})
	t.Run("inclusive time ordering tie breaks versions ownership and truncation", func(t *testing.T) {
		s, r := newStore(t)
		for i, off := range []time.Duration{time.Hour, 0, time.Minute, time.Minute} {
			e := event
			e.Timestamp, e.ResourceVersion = base.Add(off), string(rune('1'+i))
			e.Data = map[string]any{"unretained": "synthetic-payload-canary"}
			appendEvent(t, s, e)
		}
		for _, limit := range []int{1, 3, 4, 5} {
			q := query
			q.Limit = limit
			got, err := r.ReadHistoryEvents(ctx, q)
			if err != nil || len(got.Events) != min(limit, 4) || got.Truncated != (limit < 4) {
				t.Fatalf("limit %d: page=%+v error=%v", limit, got, err)
			}
			for i, e := range got.Events {
				want := []string{"2", "3", "4", "1"}[i]
				if e.ResourceVersion != want || e.ID == 0 || e.UID != "same-uid" {
					t.Fatalf("event identity/order lost: %+v", got.Events)
				}
			}
			encoded, err := json.Marshal(got)
			if err != nil || strings.Contains(string(encoded), "canary") || strings.Contains(string(encoded), `"data"`) {
				t.Fatal("event page exposed payload")
			}
			got.Events[0].Name = "caller mutation"
			again, err := r.ReadHistoryEvents(ctx, q)
			if err != nil || again.Events[0].Name != "same" {
				t.Fatal("returned page aliases retained data")
			}
		}
	})
	t.Run("all namespaces only inside the selected cluster", func(t *testing.T) {
		s, r := newStore(t)
		for _, scope := range [][2]string{{"prod", "a"}, {"prod", "b"}, {"other", "c"}} {
			e := event
			e.ClusterID, e.Namespace = scope[0], scope[1]
			appendEvent(t, s, e)
		}
		q := query
		q.Namespace = ""
		got, err := r.ReadHistoryEvents(ctx, q)
		if err != nil || len(got.Events) != 2 || got.Truncated {
			t.Fatalf("namespace union=%+v error=%v", got, err)
		}
	})
	t.Run("aggregate and oversized metadata fail without partial page", func(t *testing.T) {
		s, r := newStore(t)
		appendEvent(t, s, event)
		appendEvent(t, s, event)
		q := query
		q.MaxBytes = 1024 // Each event fits alone, but their conservative sum does not.
		got, err := r.ReadHistoryEvents(ctx, q)
		if !errors.Is(err, graph.ErrHistoryEventLimit) || !reflect.DeepEqual(got, graph.HistoryEventPage{}) {
			t.Fatalf("aggregate must fail closed: page=%+v error=%v", got, err)
		}
		e := event
		e.Timestamp = base.Add(-time.Hour)
		e.Name = strings.Repeat("oversized", graph.MaxHistoryBytes/8)
		appendEvent(t, s, e)
		q.From = e.Timestamp
		got, err = r.ReadHistoryEvents(ctx, q)
		if !errors.Is(err, graph.ErrHistoryEventLimit) || !reflect.DeepEqual(got, graph.HistoryEventPage{}) {
			t.Fatal("oversized first row must not return a partial page")
		}
	})
	t.Run("cancelled and invalid queries fail without partial page", func(t *testing.T) {
		_, r := newStore(t)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		got, err := r.ReadHistoryEvents(cancelled, query)
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, graph.HistoryEventPage{}) {
			t.Fatal("cancelled read returned success")
		}
		for _, mutate := range []func(*graph.HistoryEventQuery){
			func(q *graph.HistoryEventQuery) { q.ClusterID = "other:scope" },
			func(q *graph.HistoryEventQuery) { q.ClusterID = "other/scope" },
			func(q *graph.HistoryEventQuery) { q.ClusterID = "nul\x00scope" },
			func(q *graph.HistoryEventQuery) { q.ClusterID = strings.Repeat("a", 254) },
			func(q *graph.HistoryEventQuery) { q.Namespace = "nul\x00scope" },
			func(q *graph.HistoryEventQuery) { q.Namespace = strings.Repeat("a", 254) },
			func(q *graph.HistoryEventQuery) { q.From = time.Time{} },
			func(q *graph.HistoryEventQuery) { q.To = q.From },
			func(q *graph.HistoryEventQuery) { q.From = q.To.Add(time.Second) },
			func(q *graph.HistoryEventQuery) { q.Limit = -1 },
			func(q *graph.HistoryEventQuery) { q.Limit = graph.MaxHistoryEvents + 1 },
			func(q *graph.HistoryEventQuery) { q.MaxBytes = -1 },
			func(q *graph.HistoryEventQuery) { q.MaxBytes = graph.MaxHistoryBytes + 1 },
		} {
			q := query
			mutate(&q)
			got, err := r.ReadHistoryEvents(ctx, q)
			if !errors.Is(err, graph.ErrHistoryEventQuery) || !reflect.DeepEqual(got, graph.HistoryEventPage{}) {
				t.Fatalf("invalid query accepted: %+v error=%v", q, err)
			}
		}
	})
}
