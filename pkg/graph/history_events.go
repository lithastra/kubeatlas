// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"errors"
	"strings"
	"time"
)

// HistoryEventReader is an optional, bounded metadata projection, not a change
// to GraphStore v2 or ListEvents. Exactly one cluster is selected BEFORE limits
// and counts. Empty ClusterID means only unlabelled rows, never all clusters.
// Those rows may be standalone or legacy unlabelled federation observations;
// callers must not infer their provenance or reassign them to a named member.
//
// This capability is NOT authorization. Callers must authorize before reading;
// visibility wrappers must explicitly implement it, never unwrap or fall back
// to the unscoped ListEvents. Rows are ordered by (Timestamp, ID), oldest first.
// Data and Secret-object events are excluded, even in legacy storage. No partial
// page is returned on error. Truncated=false proves only that this bounded read
// did not omit matching retained rows, NOT historical collection completeness,
// durable retention, snapshot availability, or absence of changes in Kubernetes.
type HistoryEventReader interface {
	ReadHistoryEvents(context.Context, HistoryEventQuery) (HistoryEventPage, error)
}

const (
	HistoryReadTimeout   = 5 * time.Second
	DefaultHistoryEvents = 200
	MaxHistoryEvents     = 1000
	DefaultHistoryBytes  = 1 << 20
	MaxHistoryBytes      = 2 << 20
)

var (
	ErrHistoryEventQuery = errors.New("history events: invalid query")
	ErrHistoryEventLimit = errors.New("history events: metadata budget exceeded")
	ErrHistoryEventScope = errors.New("history events: invalid projected scope")
)

// HistoryEventQuery selects an inclusive retained time window. Namespace empty
// means every namespace in this one cluster. Zero budgets use defaults; invalid
// values fail, never clamp. MaxBytes bounds a conservative JSON-size estimate,
// not exact Go heap size or the final HTTP response. Retention policy and access
// control belong to the caller; this query cannot establish a coverage window.
type HistoryEventQuery struct {
	ClusterID string
	Namespace string
	From      time.Time
	To        time.Time
	Limit     int
	MaxBytes  int
}

func (q HistoryEventQuery) Normalize() (HistoryEventQuery, error) {
	if q.Limit == 0 {
		q.Limit = DefaultHistoryEvents
	}
	if q.MaxBytes == 0 {
		q.MaxBytes = DefaultHistoryBytes
	}
	if len(q.ClusterID) > 253 || strings.ContainsAny(q.ClusterID, "/:\x00") || len(q.Namespace) > 253 || strings.ContainsRune(q.Namespace, '\x00') ||
		q.From.IsZero() || q.To.IsZero() || !q.From.Before(q.To) ||
		q.Limit < 1 || q.Limit > MaxHistoryEvents || q.MaxBytes < 1 || q.MaxBytes > MaxHistoryBytes {
		return q, ErrHistoryEventQuery
	}
	return q, nil
}

// HistoryEvent deliberately has no Data field. It is retained event metadata,
// not a historical object, field-level diff, graph edge, or snapshot.
type HistoryEvent struct {
	ID              int64     `json:"id"`
	Timestamp       time.Time `json:"ts"`
	ClusterID       string    `json:"clusterId,omitempty"`
	Namespace       string    `json:"namespace"`
	Kind            string    `json:"kind"`
	UID             string    `json:"uid,omitempty"`
	Name            string    `json:"name"`
	EventType       EventType `json:"eventType"`
	ResourceVersion string    `json:"resourceVersion,omitempty"`
}

func ProjectHistoryEvent(e ResourceEvent) HistoryEvent {
	return HistoryEvent{
		ID: e.ID, Timestamp: e.Timestamp, ClusterID: e.ClusterID,
		Namespace: e.Namespace, Kind: e.Kind, UID: e.UID, Name: e.Name,
		EventType: e.EventType, ResourceVersion: e.ResourceVersion,
	}
}

type HistoryEventPage struct {
	Events    []HistoryEvent `json:"events"`
	Truncated bool           `json:"truncated"`
}

// HistoryEventPageBuilder enforces the same metadata budgets for both stores.
// It is read-local. Discard Page after any error. Adapters filter and sort before
// Add; one extra matching row establishes truncation without exposing its data.
type HistoryEventPageBuilder struct {
	Page      HistoryEventPage
	query     HistoryEventQuery
	remaining int
}

func NewHistoryEventPageBuilder(q HistoryEventQuery) (*HistoryEventPageBuilder, error) {
	q, err := q.Normalize()
	if err != nil {
		return nil, err
	}
	return &HistoryEventPageBuilder{
		Page: HistoryEventPage{Events: []HistoryEvent{}}, query: q, remaining: q.MaxBytes,
	}, nil
}

func (b *HistoryEventPageBuilder) Add(ctx context.Context, e HistoryEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.ClusterID != b.query.ClusterID || (b.query.Namespace != "" && e.Namespace != b.query.Namespace) ||
		e.Timestamp.Before(b.query.From) || e.Timestamp.After(b.query.To) || e.Kind == "Secret" {
		return ErrHistoryEventScope
	}
	if len(b.Page.Events) == b.query.Limit {
		b.Page.Truncated = true
		return nil
	}
	// Fixed fields, timestamp, punctuation, and worst-case JSON string escaping.
	if b.remaining < 512 {
		return ErrHistoryEventLimit
	}
	b.remaining -= 512
	for _, value := range []string{e.ClusterID, e.Namespace, e.Kind, e.UID, e.Name, string(e.EventType), e.ResourceVersion} {
		if len(value) > b.remaining/6 {
			return ErrHistoryEventLimit
		}
		b.remaining -= 6 * len(value)
	}
	b.Page.Events = append(b.Page.Events, e)
	return nil
}
