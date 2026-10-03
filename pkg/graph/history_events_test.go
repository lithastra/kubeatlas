// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestHistoryEventProjectionContract(t *testing.T) {
	base := time.Date(2026, 9, 30, 1, 0, 0, 0, time.UTC)
	q := HistoryEventQuery{ClusterID: "prod", Namespace: "demo", From: base, To: base.Add(time.Hour)}
	normal, err := q.Normalize()
	if err != nil || normal.Limit != DefaultHistoryEvents || normal.MaxBytes != DefaultHistoryBytes {
		t.Fatalf("defaults=%+v error=%v", normal, err)
	}
	e := HistoryEvent{Timestamp: base, ClusterID: "prod", Namespace: "demo", Kind: "Pod", Name: "p"}
	for name, mutate := range map[string]func(*HistoryEvent){
		"cluster":   func(e *HistoryEvent) { e.ClusterID = "other" },
		"namespace": func(e *HistoryEvent) { e.Namespace = "other" },
		"Secret":    func(e *HistoryEvent) { e.Kind = "Secret" },
		"before":    func(e *HistoryEvent) { e.Timestamp = base.Add(-time.Second) },
		"after":     func(e *HistoryEvent) { e.Timestamp = q.To.Add(time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			b, err := NewHistoryEventPageBuilder(q)
			if err != nil {
				t.Fatal(err)
			}
			bad := e
			mutate(&bad)
			if err := b.Add(context.Background(), bad); !errors.Is(err, ErrHistoryEventScope) || len(b.Page.Events) != 0 {
				t.Fatal("builder accepted out-of-scope metadata")
			}
		})
	}
	t.Run("byte budget includes worst case escaping and exact boundary", func(t *testing.T) {
		e := e
		e.Name = "\x00<&\"\\\n"
		// An independent upper bound from the documented fixed and scalar fields.
		cost := 512 + 6*(len(e.ClusterID)+len(e.Namespace)+len(e.Kind)+len(e.Name))
		q := q
		q.MaxBytes = cost
		b, err := NewHistoryEventPageBuilder(q)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Add(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(b.Page)
		if err != nil || len(body) > cost {
			t.Fatal("charge underestimated JSON size")
		}
		q.MaxBytes--
		b, err = NewHistoryEventPageBuilder(q)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Add(context.Background(), e); !errors.Is(err, ErrHistoryEventLimit) {
			t.Fatal("budget accepted one byte below the conservative charge")
		}
	})
	t.Run("cancelled builder never returns successful metadata", func(t *testing.T) {
		b, err := NewHistoryEventPageBuilder(q)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := b.Add(ctx, e); !errors.Is(err, context.Canceled) || len(b.Page.Events) != 0 {
			t.Fatal("cancelled builder accepted metadata")
		}
	})
}

// A legacy visibility wrapper must not accidentally advertise unrestricted
// history reads just by embedding the unchanged GraphStore interface.
type legacyHistoryVisibilityWrapper struct{ GraphStore }

func TestHistoryEventCapabilityIsExplicit(t *testing.T) {
	if _, ok := any(legacyHistoryVisibilityWrapper{}).(HistoryEventReader); ok {
		t.Fatal("optional history capability leaked through legacy GraphStore")
	}
}
