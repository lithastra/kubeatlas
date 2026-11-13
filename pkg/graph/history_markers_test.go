// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
package graph

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestHistoryMarkerCapabilityAndBudgets(t *testing.T) {
	if _, ok := any(legacyHistoryVisibilityWrapper{}).(HistoryMarkerReader); ok {
		t.Fatal("capability leaked through GraphStore embedding")
	}
	b, err := NewHistoryMarkerBuilder("prod")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Add(context.Background(), SnapshotMeta{ClusterID: "hidden"}); !errors.Is(err, ErrHistoryEventScope) {
		t.Fatal("foreign marker accepted")
	}
	b, _ = NewHistoryMarkerBuilder("prod")
	if err := b.Add(context.Background(), SnapshotMeta{ClusterID: "prod", Trigger: SnapshotTrigger(strings.Repeat("oversized", MaxHistoryBytes/8))}); !errors.Is(err, ErrHistoryEventLimit) {
		t.Fatal("oversized scalar accepted")
	}
	b, _ = NewHistoryMarkerBuilder("prod")
	for range MaxHistoryEvents {
		if err := b.Add(context.Background(), SnapshotMeta{ClusterID: "prod"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Add(context.Background(), SnapshotMeta{ClusterID: "prod"}); !errors.Is(err, ErrHistoryEventLimit) {
		t.Fatal("row overflow accepted")
	}

}
