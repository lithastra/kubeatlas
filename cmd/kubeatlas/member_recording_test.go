// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/snapshot"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type memberRecordingEventSink struct {
	store                         *memory.Store
	entered, release, westWritten chan struct{}
	eastOnce, westOnce            sync.Once
}

func (s *memberRecordingEventSink) AppendEvent(ctx context.Context, e graph.ResourceEvent) error {
	if e.ClusterID == "east" {
		s.eastOnce.Do(func() { close(s.entered) })
		<-s.release
	}
	err := s.store.AppendEvent(ctx, e)
	if e.ClusterID == "west" && err == nil {
		s.westOnce.Do(func() { close(s.westWritten) })
	}
	return err
}

func TestMemberFactoryBindsRecordingAndJoinsOnlyOwnPendingWrites(t *testing.T) {
	store := memory.New()
	sink := &memberRecordingEventSink{store: store, entered: make(chan struct{}), release: make(chan struct{}), westWritten: make(chan struct{})}
	writer := snapshot.New(sink, snapshot.Config{Workers: 2, QueueSize: 4}, nil)
	writer.Start(context.Background())
	t.Cleanup(writer.Stop)
	tracker := operations.NewCoverageTracker(time.Minute)
	// The production member sink must override this legacy shared-writer option.
	factory := memberInformerFactory([]discovery.InformerOption{discovery.WithCoverage(tracker), discovery.WithGVRs([]schema.GroupVersionResource{memberPods}), discovery.WithSnapshotSink(writer)}, tracker, nil, extractor.Default(), discovery.NewDynamicMetrics(), nil, writer)
	start := func(cluster string) func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		client := memberDynamicClient(t)
		go func() { done <- factory(cluster, client, memberMetadata{}, store).Start(ctx) }()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				select {
				case err := <-done:
					if err != nil && !errors.Is(err, context.Canceled) {
						t.Errorf("member recording exit: %v", err)
					}
				case <-time.After(4 * time.Second):
					t.Error("member did not join its recording")
				}
			})
		}
		t.Cleanup(stop)
		return stop
	}
	stopEast, stopWest := start("east"), start("west")
	var released sync.Once
	release := func() { released.Do(func() { close(sink.release) }) }
	t.Cleanup(release)
	select {
	case <-sink.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("member core event never reached shared writer")
	}
	select {
	case <-sink.westWritten:
	case <-time.After(3 * time.Second):
		t.Fatal("blocked east event prevented west recording")
	}
	availability := snapshot.NewAnalysisAvailabilityReader(nil, writer, snapshot.AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true, Federated: true, Retention: time.Hour})
	read := func(cluster string) operations.HistoryAvailability {
		t.Helper()
		e, err := availability.SnapshotAvailability(context.Background(), cluster)
		if err != nil {
			t.Fatal(err)
		}
		return e.History
	}
	awaitMember(t, "west event acknowledged", func() bool { return !slices.Contains(read("west").Reasons, operations.HistoryDeliveryPending) })
	east := read("east")
	if east.State != operations.HistoryRecording || east.Coverage != operations.CoverageUnknown || !slices.Contains(east.Reasons, operations.HistoryDeliveryPending) || read("west").State != operations.HistoryRecording {
		t.Fatal("member recording was inferred from shared queue state")
	}
	stopped := make(chan struct{})
	go func() { stopEast(); close(stopped) }()
	awaitMember(t, "east stopped recording while draining", func() bool { return read("east").State == operations.HistoryStopped })
	if err := writer.ForMember("east").Begin(context.Background()); !errors.Is(err, snapshot.ErrMemberRecordingStarted) {
		t.Fatal("same-name member overlapped old pending recording")
	}
	select {
	case <-stopped:
		t.Fatal("member returned before its event was committed")
	case <-time.After(30 * time.Millisecond):
	}
	if read("west").State != operations.HistoryRecording {
		t.Fatal("east removal stopped another member's recording")
	}
	release()
	<-stopped
	start("east")
	awaitMember(t, "fresh east recording lifetime", func() bool {
		h := read("east")
		return h.State == operations.HistoryRecording && h.ObservationStartedAt.After(east.ObservationStartedAt)
	})
	stopWest()
}
