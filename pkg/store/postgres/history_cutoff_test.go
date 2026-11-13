// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"testing"
	"time"
)

func TestHistoryCutoffMatchesStoredTimestampPrecision(t *testing.T) {
	// PostgreSQL stores microseconds. Check strict prune and inclusive read
	// predicates on the same representable instants across the Unix epoch.
	epoch := time.Unix(0, 0)
	for offset := -2 * time.Microsecond; offset <= 2*time.Microsecond; offset += time.Nanosecond {
		cutoff := epoch.Add(offset)
		bound := ceilHistoryTimestamp(cutoff)
		if !bound.Equal(bound.Truncate(time.Microsecond)) {
			t.Fatal("bound is not representable in PostgreSQL")
		}
		for delta := -3 * time.Microsecond; delta <= 3*time.Microsecond; delta += time.Microsecond {
			event := epoch.Add(delta)
			if event.Before(cutoff) != event.Before(bound) {
				t.Fatalf("strict cutoff changed at %s for event %s", offset, delta)
			}
		}
	}
}
