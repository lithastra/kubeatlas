package graph

import "time"

// Snapshot history types (F-111 / P3-T2).
//
// These describe the wire + storage shape of the resource-event
// stream the Tier 2 backend persists. They live in pkg/graph next
// to the GraphStore interface because the snapshot-writer methods
// (AppendEvent / WriteSnapshotMeta / QueryEvents) are added to that
// interface in the P3-T2 implementation step — the types are split
// into this file purely to keep store.go focused on the interface.
//
// Snapshots are a Tier 2 feature (invariant 2.2). The memory store
// satisfies the interface with a small bounded ring buffer for test
// support only; the /api/v1/snapshots endpoints return 503 on
// Tier 1. Nothing here is a "lightweight Tier 1 snapshot".

// EventType classifies a resource_events row. The three values map
// 1:1 to the K8s informer's Add / Update / Delete callbacks and to
// the CHECK constraint in migrate/005_snapshots.sql.
type EventType string

const (
	EventTypeAdd    EventType = "add"
	EventTypeUpdate EventType = "update"
	EventTypeDelete EventType = "delete"
)

// SnapshotTrigger classifies why a snapshot_meta row was written.
// Mirrors the CHECK constraint in migrate/005_snapshots.sql.
type SnapshotTrigger string

const (
	// SnapshotTriggerPeriodic — the Helm-configured CronJob cadence.
	SnapshotTriggerPeriodic SnapshotTrigger = "periodic"
	// SnapshotTriggerManual — an operator-invoked `kubeatlas snapshot`.
	SnapshotTriggerManual SnapshotTrigger = "manual"
	// SnapshotTriggerStartup — the marker written once on process boot
	// so the diff endpoint has an anchor before the first periodic run.
	SnapshotTriggerStartup SnapshotTrigger = "startup"
)

// ResourceEvent is one append-only row in resource_events: a single
// observed add/update/delete of one K8s resource.
//
// The stream is INSERT-only. A correction is a compensating event,
// never an UPDATE of an existing row (anti-pattern guard in P3-T2).
//
// ID and Timestamp are assigned by the store on insert; callers
// leave them zero. Data carries the full resource payload for add /
// update events and is nil for delete events (the resource is gone;
// only its identity is recorded).
type ResourceEvent struct {
	ID              int64          `json:"id"`
	Timestamp       time.Time      `json:"ts"`
	ClusterID       string         `json:"clusterId,omitempty"`
	Namespace       string         `json:"namespace"`
	Kind            string         `json:"kind"`
	UID             string         `json:"uid,omitempty"`
	Name            string         `json:"name"`
	EventType       EventType      `json:"eventType"`
	ResourceVersion string         `json:"resourceVersion,omitempty"`
	Data            map[string]any `json:"data,omitempty"`
}

// SnapshotMeta is one row in snapshot_meta: a periodic full-sync
// marker the diff endpoint anchors time-window queries against.
//
// ID and Timestamp are assigned by the store on insert.
type SnapshotMeta struct {
	ID            int64           `json:"id"`
	Timestamp     time.Time       `json:"ts"`
	ClusterID     string          `json:"clusterId,omitempty"`
	ResourceCount int64           `json:"resourceCount"`
	EdgeCount     int64           `json:"edgeCount"`
	DurationMS    int64           `json:"durationMs"`
	Trigger       SnapshotTrigger `json:"trigger"`
}
