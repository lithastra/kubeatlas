// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

var inventoryPod = APIResourceDescriptor{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true, List: true, Watch: true}

func TestAPIInventoryLifecycleOwnershipAndBounds(t *testing.T) {
	tracker := NewCoverageTracker(time.Minute)
	s, err := tracker.BeginAPIInventory("prod")
	if err != nil {
		t.Fatal(err)
	}
	read := func(at time.Time) *APIInventoryEvidence { return requireCoverage(t, tracker, "prod", at).APIInventory }
	if read(coverageTime).State != APIInventoryUnknown || read(coverageTime).Revision != 0 {
		t.Fatal("unqueried inventory became complete")
	}
	resources := []APIResourceDescriptor{inventoryPod}
	if err := s.Record(APIInventoryComplete, resources, false, coverageTime); err != nil {
		t.Fatal(err)
	}
	resources[0].Kind = "input mutation"
	if read(coverageTime).Resources[0].Kind != "Pod" {
		t.Fatal("record aliases caller")
	}
	view := read(coverageTime)
	view.Resources[0].Kind = "output mutation"
	if read(coverageTime).Resources[0].Kind != "Pod" {
		t.Fatal("snapshot aliases inventory")
	}
	if read(coverageTime.Add(APIInventoryMaxAge-time.Nanosecond)).Stale || !read(coverageTime.Add(APIInventoryMaxAge)).Stale {
		t.Fatal("wrong freshness boundary")
	}
	for _, state := range []APIInventoryState{APIInventoryPartial, APIInventoryDenied, APIInventoryFailed} {
		if err := s.Record(state, nil, false, coverageTime); err != nil {
			t.Fatal(err)
		}
		if read(coverageTime).State != state || len(read(coverageTime).Resources) != 0 {
			t.Fatal("stale success retained as current result")
		}
	}
	before := *read(coverageTime)
	beginCoverage(t, tracker, "prod", coveragePods)
	beginSource(t, tracker, "prod", CoverageSourceCRD)
	if !reflect.DeepEqual(before, *read(coverageTime)) {
		t.Fatal("watch attachment reset discovery")
	}
	replacement, err := tracker.BeginAPIInventory("prod")
	if err != nil {
		t.Fatal(err)
	}
	before = *read(coverageTime)
	s.Stop()
	if err := s.Record(APIInventoryComplete, []APIResourceDescriptor{inventoryPod}, false, coverageTime); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, *read(coverageTime)) {
		t.Fatal("old collector changed replacement")
	}
	replacement.Stop()
	if err := replacement.Record(APIInventoryComplete, []APIResourceDescriptor{inventoryPod}, false, coverageTime); err != nil {
		t.Fatal(err)
	}
	if read(coverageTime).State != APIInventoryUnknown || !read(coverageTime).Stopped {
		t.Fatal("stopped collector accepted success")
	}
	for i := 1; i < MaxCoverageClusters; i++ {
		if _, err := tracker.BeginAPIInventory(fmt.Sprintf("cluster%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tracker.BeginAPIInventory("overflow"); !errors.Is(err, ErrCoverageScope) {
		t.Fatal("inventory bypassed cluster cap")
	}
	if _, err := tracker.Begin("overflow", nil, coverageTime); !errors.Is(err, ErrCoverageScope) {
		t.Fatal("core bypassed inventory-owned cluster cap")
	}
}

func TestAPIInventoryRejectsUnboundedOrInvalidInput(t *testing.T) {
	tracker := NewCoverageTracker(time.Minute)
	s, err := tracker.BeginAPIInventory("")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state", "unknown", "time", "duplicate", "bad_type", "limit", "failed_data", "resource_cap", "version_cap"} {
		t.Run(name, func(t *testing.T) {
			state, resources, limited, at := APIInventoryComplete, []APIResourceDescriptor{inventoryPod}, false, coverageTime
			switch name {
			case "state":
				state = "raw-error-canary"
			case "unknown":
				state = APIInventoryUnknown
			case "time":
				at = time.Time{}
			case "duplicate":
				resources = append(resources, inventoryPod)
			case "bad_type":
				resources[0].Resource = "pods/status"
			case "limit":
				limited = true
			case "failed_data":
				state = APIInventoryFailed
			case "resource_cap":
				resources = make([]APIResourceDescriptor, MaxInventoryResources+1)
			case "version_cap":
				for i := range MaxInventoryGroupVersions {
					r := inventoryPod
					r.Group = fmt.Sprintf("group%d.example.com", i)
					resources = append(resources, r)
				}
			}
			if err := s.Record(state, resources, limited, at); !errors.Is(err, ErrCoverageScope) {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestAPIInventoryConcurrentRecordAndCapture(t *testing.T) {
	tracker := NewCoverageTracker(time.Minute)
	s, err := tracker.BeginAPIInventory("")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				if err := s.Record(APIInventoryComplete, []APIResourceDescriptor{inventoryPod}, false, coverageTime); err != nil {
					t.Error(err)
				}
				if _, err := tracker.SnapshotCoverage(context.Background(), "", nil); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	s.Stop()
	if got := requireCoverage(t, tracker, "", coverageTime).APIInventory; got.Revision != 160 || !got.Stopped {
		t.Fatalf("lost concurrent evidence: %+v", got)
	}
}
