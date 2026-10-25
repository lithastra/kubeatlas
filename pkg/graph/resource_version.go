// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import "context"

// ResourceVersionDeleter is an optional atomic mutation capability for observed
// object deletion and repair workers. GraphStore v2 remains unchanged.
// Implementations must compare the
// full identity, API version, UID and resourceVersion and cascade incident edges
// in the same critical section/transaction. A mismatch or missing row is a
// successful no-op. Invalid expectations and cancelled calls must not mutate.
type ResourceVersionDeleter interface {
	DeleteResourceVersion(context.Context, Resource) (bool, error)
}

// ValidResourceVersion excludes reference-only and unversioned graph nodes.
func ValidResourceVersion(r Resource) bool {
	return r.Kind != "" && r.Kind != "Secret" && r.Name != "" && r.GroupVersion != "" && r.UID != "" && r.ResourceVersion != ""
}

// SameResourceVersion compares the object and private writer receipts, never
// mutable payload maps. A fresh writer invalidates an older deletion receipt.
func SameResourceVersion(a, b Resource) bool {
	return a.ID() == b.ID() && a.ClusterID == b.ClusterID && a.GroupVersion == b.GroupVersion && a.UID == b.UID && a.ResourceVersion == b.ResourceVersion && a.Origin == b.Origin
}
