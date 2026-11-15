// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package gatekeeper

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/client-go/tools/cache"
)

func TestTemplateOwnerReplacementRejectsOldTombstonesAndRetainsData(t *testing.T) {
	client, template := coverageClient()
	_, object := coverageObjects()
	object.SetUID("constraint-object-uid")
	if err := client.Tracker().Update(coverageConstraintGVR, object, ""); err != nil {
		t.Fatal(err)
	}
	tracker := operations.NewCoverageTracker(time.Minute)
	source, _ := tracker.BeginSource("", operations.CoverageSourceGatekeeper, time.Now())
	meta, _ := source.BeginType(constraintTemplateGVR, time.Now())
	s := memory.New()
	manager := discovery.NewDynamicInformerManager(client)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Start(ctx) }()
	d := New(client, s, nilRegistry{}, manager)
	d.coverageSession, d.metaCoverage = source, meta
	d.memberRepair = discovery.NewStandaloneRepair(client, s)
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("manager did not join")
		}
		d.repairs.Wait()
	})
	waitFor(t, 3*time.Second, "dynamic manager startup", manager.Started)
	template.SetUID("original-template-uid")
	d.onTemplate(ctx, template)
	id := "/K8sRequiredLabels/test-labels"
	waitFor(t, 3*time.Second, "original template receipt", func() bool {
		r, err := s.GetResource(ctx, id)
		return err == nil && r.Origin.OwnerUID == "original-template-uid"
	})
	old, _ := s.GetResource(ctx, id)
	replacement := template.DeepCopy()
	replacement.SetUID("replacement-template-uid")
	d.onTemplate(ctx, replacement)
	waitFor(t, 3*time.Second, "replacement observed current object", func() bool {
		r, err := s.GetResource(ctx, id)
		return err == nil && r.Origin.OwnerUID == "replacement-template-uid"
	})
	d.onTemplateDelete(&cache.DeletedFinalStateUnknown{Obj: template})
	if !manager.Has(coverageConstraintGVR) {
		t.Fatal("old UID tombstone retired replacement")
	}
	unknown := replacement.DeepCopy()
	unknown.SetUID("")
	d.onTemplateDelete(unknown)
	if !manager.Has(coverageConstraintGVR) {
		t.Fatal("unidentified tombstone retired proven owner")
	}
	d.onTemplateDelete(cache.DeletedFinalStateUnknown{Obj: replacement})
	if manager.Has(coverageConstraintGVR) {
		t.Fatal("matching template UID failed to retire endpoint")
	}
	got, err := s.GetResource(ctx, id)
	if err != nil || !got.Origin.Matches(got) {
		t.Fatal("template retirement deleted source data", err)
	}
	capture, _ := tracker.SnapshotCoverage(ctx, "", nil)
	if graph.OriginQualification(got, capture.Origins) != "retired" || graph.OriginQualification(old, capture.Origins) != "replaced" {
		t.Fatal("retired/replaced ownership not distinguished")
	}
	events, _ := s.ListEvents(ctx, "", time.Time{}, time.Now().Add(time.Minute))
	if len(events) != 0 {
		t.Fatal("metadata retirement manufactured delete history")
	}
}
