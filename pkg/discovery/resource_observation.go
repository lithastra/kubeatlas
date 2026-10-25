// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// UpsertObservedResource applies dynamic-source receipts without enabling
// history. Callers must hold their scope's shared MemberRepair resource lock
// through this operation and subsequent extraction, as with ResourceHistory.
// Unchanged overlapping delivery preserves the stored writer; a fresh lifetime
// of that same source can refresh its receipt. Skipping a resource write must
// never skip the caller's edge extraction. This is not cross-watch ordering or
// all-source delivery accounting, and a missing row remains a new observation.
func UpsertObservedResource(ctx context.Context, store graph.GraphStore, r graph.Resource) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if store == nil {
		return false, errors.New("missing observation store")
	}
	if graph.ValidResourceVersion(r) {
		previous, err := store.GetResource(ctx, r.ID())
		missing := errors.As(err, new(graph.ErrNotFound))
		if err != nil && !missing {
			return false, fmt.Errorf("read observation receipt: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !missing && preserveResourceReplay(previous, r) {
			return false, nil
		}
	}
	if err := store.UpsertResource(ctx, r); err != nil {
		return false, err
	}
	return true, nil
}

// Both history modes use the stored row, never an informer-local source cache.
// Opaque resourceVersions are compared only for equality. Scope and API-version
// changes cannot reuse another representation's writer receipt.
func preserveResourceReplay(previous, r graph.Resource) bool {
	if !graph.ValidResourceVersion(r) || previous.ClusterID != r.ClusterID || previous.ID() != r.ID() || previous.UID != r.UID || previous.ResourceVersion != r.ResourceVersion || previous.GroupVersion != r.GroupVersion {
		return false
	}
	return !r.Origin.Matches(r) || previous.Origin == r.Origin ||
		(previous.Origin.Matches(previous) && previous.Origin.Source != r.Origin.Source)
}

// DeleteObservedResource consumes an actual object deletion notification without
// enabling history. Callers must hold the same scope/resource lock as upserts.
// A tombstone may carry an older opaque version for the same UID/API group;
// deletion compares the full current stored receipt atomically, including origin.
// Missing/unversioned rows and UID/scope/group mismatches are retained. A store
// without conditional deletion must never fall back to deleting by name.
func DeleteObservedResource(ctx context.Context, store graph.GraphStore, r graph.Resource) (bool, error) {
	_, deleted, err := deleteObservedResource(ctx, store, r)
	return deleted, err
}

// Return the committed stored receipt for metadata-only history/notifications.
// Source retirement, endpoint disappearance and age are not deletion evidence.
func deleteObservedResource(ctx context.Context, store graph.GraphStore, r graph.Resource) (graph.Resource, bool, error) {
	if err := ctx.Err(); err != nil {
		return graph.Resource{}, false, err
	}
	if store == nil {
		return graph.Resource{}, false, errors.New("missing observation store")
	}
	if !graph.ValidResourceVersion(r) {
		return graph.Resource{}, false, nil
	}
	previous, err := store.GetResource(ctx, r.ID())
	if err := ctx.Err(); err != nil {
		return graph.Resource{}, false, err
	}
	if errors.As(err, new(graph.ErrNotFound)) {
		return graph.Resource{}, false, nil
	}
	if err != nil {
		return graph.Resource{}, false, fmt.Errorf("read deletion receipt: %w", err)
	}
	observedGV, observedErr := schema.ParseGroupVersion(r.GroupVersion)
	storedGV, storedErr := schema.ParseGroupVersion(previous.GroupVersion)
	if observedErr != nil || storedErr != nil || observedGV.Version == "" || storedGV.Version == "" || observedGV.Group != storedGV.Group {
		return graph.Resource{}, false, nil
	}
	if !graph.ValidResourceVersion(previous) || previous.ClusterID != r.ClusterID || previous.Namespace != r.Namespace || previous.Kind != r.Kind || previous.Name != r.Name || previous.UID != r.UID {
		return graph.Resource{}, false, nil
	}
	deleter, ok := store.(graph.ResourceVersionDeleter)
	if !ok {
		return graph.Resource{}, false, errors.New("observation requires atomic resource deletion")
	}
	deleted, err := deleter.DeleteResourceVersion(ctx, previous)
	return previous, deleted, err
}
