// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// ResourceHistory coordinates metadata history for overlapping collectors in
// one named member or an explicitly bound unlabelled standalone scope.
// Callers MUST hold their shared MemberRepair resource lock through this operation
// and subsequent edge extraction. The graph is the receipt: no second object cache,
// queue, worker pool, or API request is introduced. This is observation history,
// not an ordered Kubernetes audit log; resourceVersions are opaque.
type ResourceHistory struct {
	store      graph.GraphStore
	cluster    string
	sink       SnapshotSink
	standalone bool
}

// NewResourceHistory binds one member's collectors to the same recording token.
func NewResourceHistory(store graph.GraphStore, cluster string, sink SnapshotSink) *ResourceHistory {
	return &ResourceHistory{store: store, cluster: cluster, sink: sink}
}

// NewStandaloneResourceHistory explicitly binds only unlabelled rows to the
// existing standalone writer. It never attributes legacy rows to a member or
// qualifies the provenance/completeness of their retained history. An empty
// member name passed to NewResourceHistory remains invalid.
func NewStandaloneResourceHistory(store graph.GraphStore, sink SnapshotSink) *ResourceHistory {
	return &ResourceHistory{store: store, sink: sink, standalone: true}
}

// Upsert records only a successfully committed change in observed UID/version.
// A served-version switch can update the graph without creating a history event.
// Replays still reach the caller's extractor, allowing failed/missing edge repair.
// A late watch upsert after deletion can reinsert the same UID: the deleted row
// no longer supplies a receipt. There is no cross-watch order or tombstone ledger.
func (h *ResourceHistory) Upsert(ctx context.Context, r graph.Resource, eventType graph.EventType) (bool, error) {
	if err := h.validate(ctx, r); err != nil {
		return false, err
	}
	previous, err := h.store.GetResource(ctx, r.ID())
	missing := errors.As(err, new(graph.ErrNotFound))
	if err != nil && !missing {
		return false, fmt.Errorf("read history receipt: %w", err)
	}
	versioned := graph.ValidResourceVersion(r)
	same := !missing && versioned && previous.UID == r.UID && previous.ResourceVersion == r.ResourceVersion
	if same && previous.GroupVersion == r.GroupVersion {
		return false, nil
	}
	if err := h.store.UpsertResource(ctx, r); err != nil {
		return false, err
	}
	if versioned && !same {
		if !missing && previous.UID == r.UID {
			eventType = graph.EventTypeUpdate
		}
		if !missing && previous.UID != "" && previous.UID != r.UID {
			eventType = graph.EventTypeAdd
		}
		h.record(r, eventType)
	}
	return true, nil
}

// Delete uses the stored incarnation, then atomically compares its full receipt.
// Duplicate deletes and delayed tombstones for a replaced UID are no-ops. A
// tombstone may have an older RV or served version for the same deleted UID.
func (h *ResourceHistory) Delete(ctx context.Context, r graph.Resource) (bool, error) {
	if err := h.validate(ctx, r); err != nil {
		return false, err
	}
	previous, err := h.store.GetResource(ctx, r.ID())
	if errors.As(err, new(graph.ErrNotFound)) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read history receipt: %w", err)
	}
	if !graph.ValidResourceVersion(r) || !graph.ValidResourceVersion(previous) || previous.UID != r.UID {
		return false, nil
	}
	deleter, ok := h.store.(graph.ResourceVersionDeleter)
	if !ok {
		return false, errors.New("history requires atomic resource deletion")
	}
	deleted, err := deleter.DeleteResourceVersion(ctx, previous)
	if err == nil && deleted {
		h.RecordDelete(previous)
	}
	return deleted, err
}

// RecordDelete is used only after a repair worker has committed its atomic
// deletion under the same member resource lock. Retiring an informer is not a
// deletion and must never call this method. A nil receiver disables recording.
func (h *ResourceHistory) RecordDelete(r graph.Resource) {
	if h.accepts(r) && graph.ValidResourceVersion(r) {
		h.record(r, graph.EventTypeDelete)
	}
}

func (h *ResourceHistory) validate(ctx context.Context, r graph.Resource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !h.accepts(r) {
		return errors.New("invalid history scope or sink")
	}
	return nil
}

func (h *ResourceHistory) accepts(r graph.Resource) bool {
	return h != nil && h.store != nil && h.sink != nil &&
		(h.cluster != "" || h.standalone) && r.ClusterID == h.cluster
}

func (h *ResourceHistory) record(r graph.Resource, eventType graph.EventType) {
	h.sink.Enqueue(graph.ResourceEvent{ClusterID: r.ClusterID, Namespace: r.Namespace, Kind: r.Kind, Name: r.Name, UID: string(r.UID), ResourceVersion: r.ResourceVersion, EventType: eventType})
}
