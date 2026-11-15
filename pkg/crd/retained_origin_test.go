// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package crd

import (
	"errors"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestCRDRetirementKeepsDataAndRequiresFreshWriterReceipt(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "history-disabled"
		if enabled {
			name = "history-enabled"
		}
		t.Run(name, func(t *testing.T) {
			s := memory.New()
			meta, object := coverageObjects(t)
			object.SetUID("observed-object-uid")
			d, _, tracker, ctx := reconciliationDiscovery(t, s, object)
			d.memberRepair = discovery.NewStandaloneRepair(d.dyn, s)
			sink := &servedHistorySink{}
			if enabled {
				d.resourceHistory = discovery.NewStandaloneResourceHistory(s, sink)
			}
			d.onCRDAdd(ctx, meta)
			id := "default/Foo/demo"
			waitFor(t, 3*time.Second, "initial source receipt", func() bool {
				r, err := s.GetResource(ctx, id)
				return err == nil && r.Origin.Matches(r)
			})
			old, _ := s.GetResource(ctx, id)
			d.onCRDDelete(ctx, meta)
			capture, _ := tracker.SnapshotCoverage(ctx, "", nil)
			if graph.OriginQualification(old, capture.Origins) != "retired" {
				t.Fatal("retired owner remained current")
			}
			if got, err := s.GetResource(ctx, id); err != nil || got.Origin != old.Origin {
				t.Fatal("retirement rewrote/deleted retained data", got, err)
			}
			d.onCRDAdd(ctx, meta.DeepCopy())
			waitFor(t, 3*time.Second, "fresh same-owner observation", func() bool {
				r, err := s.GetResource(ctx, id)
				return err == nil && r.Origin.Matches(r) && r.Origin.Registration != old.Origin.Registration
			})
			d.shutdown()
			if deleted, err := s.DeleteResourceVersion(ctx, old); deleted || err != nil {
				t.Fatal("old receipt deleted fresh source observation", deleted, err)
			}
			if _, err := s.GetResource(ctx, id); errors.As(err, new(graph.ErrNotFound)) {
				t.Fatal("data not retained")
			}
			sink.mu.Lock()
			events := append([]graph.ResourceEvent(nil), sink.events...)
			sink.mu.Unlock()
			want := 0
			if enabled {
				want = 1
			}
			if len(events) != want {
				t.Fatal("source refresh fabricated history", len(events), want)
			}
			for _, event := range events {
				if event.EventType != graph.EventTypeAdd || event.Data != nil {
					t.Fatal("retirement fabricated deletion/payload", event)
				}
			}
		})
	}
}
