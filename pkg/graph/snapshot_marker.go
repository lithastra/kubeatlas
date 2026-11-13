// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
package graph

import "context"

// SnapshotMarkerRecorder atomically counts one stored member graph and appends
// its metadata marker. Empty cluster selects only unlabelled rows. Count edges
// only when both endpoints exist in that same scope; dangling and cross-member
// tuples are excluded. This optional capability does not change GraphStore v2.
//
// Callers must enforce the internal operator boundary and member visibility.
// A marker is neither a backup nor proof of drained/complete history. A timeout
// around database commit can leave an unknown outcome; never retry implicitly.
type SnapshotMarkerRecorder interface {
	RecordSnapshotMarker(context.Context, string, SnapshotTrigger) (SnapshotMeta, error)
}

// ValidSnapshotMarker accepts only an exact history scope and a supported trigger.
func ValidSnapshotMarker(cluster string, trigger SnapshotTrigger) bool {
	return ValidHistoryCluster(cluster) && (trigger == SnapshotTriggerManual || trigger == SnapshotTriggerPeriodic)
}
