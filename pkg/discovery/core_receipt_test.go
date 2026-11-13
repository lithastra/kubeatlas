// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type coreReceiptStore struct {
	*memory.Store
	reads, writes       int
	readFail, writeFail bool
	onRead              func()
}

func (s *coreReceiptStore) GetResource(ctx context.Context, id string) (graph.Resource, error) {
	s.reads++
	if s.onRead != nil {
		s.onRead()
		ctx = context.Background() // Exercise a dependency ignoring cancellation.
	}
	if s.readFail {
		return graph.Resource{}, errors.New("injected core receipt read failure")
	}
	return s.Store.GetResource(ctx, id)
}

func (s *coreReceiptStore) UpsertResource(ctx context.Context, r graph.Resource) error {
	s.writes++
	if s.writeFail {
		return errors.New("injected core receipt write failure")
	}
	return s.Store.UpsertResource(ctx, r)
}

var coreReceiptGVR = schema.GroupVersionResource{Group: "kyverno.io", Version: "v1", Resource: "clusterpolicies"}

type coreReceiptFixture struct {
	cluster                    string
	store                      *coreReceiptStore
	tracker                    *operations.CoverageTracker
	coverage                   *operations.CoverageSession
	repair                     *MemberRepair
	object                     *unstructured.Unstructured
	stored                     graph.Resource
	sink                       *resyncSink
	extractions, notifications int
}

func newCoreReceiptFixture(t *testing.T, cluster, lifecycle string) *coreReceiptFixture {
	t.Helper()
	f := &coreReceiptFixture{cluster: cluster, store: &coreReceiptStore{Store: memory.New()}, tracker: operations.NewCoverageTracker(time.Minute), sink: &resyncSink{}}
	var err error
	f.coverage, err = f.tracker.Begin(cluster, []schema.GroupVersionResource{coreReceiptGVR}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	source, err := f.tracker.BeginSource(cluster, operations.CoverageSourceCRD, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, err := source.BeginType(coreReceiptGVR, time.Now())
	owner := graph.ResourceOrigin{OwnerName: "clusterpolicies.kyverno.io", OwnerUID: "definition-uid", Group: coreReceiptGVR.Group, Version: coreReceiptGVR.Version, Resource: coreReceiptGVR.Resource, Kind: "ClusterPolicy"}
	if err != nil || !token.BindOrigin(owner) {
		t.Fatal("could not bind dynamic source", err)
	}
	f.object = resyncObject("ClusterPolicy", "retained", "object-uid", "opaque-first")
	f.object.SetAPIVersion(coreReceiptGVR.GroupVersion().String())
	f.object.SetNamespace("")
	f.stored = UnstructuredToResource(f.object, "ClusterPolicy")
	f.stored.ClusterID = cluster
	f.stored = token.StampOrigin(f.stored)
	if !f.stored.Origin.Matches(f.stored) {
		t.Fatal("fixture lacks a valid source receipt")
	}
	switch lifecycle {
	case "retired":
		source.Stop(time.Now())
	case "replaced":
		token.Stop(time.Now())
		next, err := source.BeginType(coreReceiptGVR, time.Now())
		if err != nil || !next.BindOrigin(owner) {
			t.Fatal("could not replace source registration", err)
		}
	case "unknown":
		f.stored.Origin = graph.ResourceOrigin{}
	}
	if err := f.store.Store.UpsertResource(context.Background(), f.stored); err != nil {
		t.Fatal(err)
	}
	foreign := f.stored
	foreign.ClusterID, foreign.Origin.ClusterID = "west", "west"
	if err := f.store.Store.UpsertResource(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	f.repair = NewMemberRepair(nil, f.store, cluster)
	if cluster == "" {
		f.repair = NewStandaloneRepair(nil, f.store)
	}
	return f
}

func (f *coreReceiptFixture) manager(history bool) *InformerManager {
	m := NewInformerManager(nil, f.store, WithClusterID(f.cluster), WithMemberRepair(f.repair), WithSnapshotSink(f.sink),
		WithBroadcaster(func(_, _, _, _ string) { f.notifications++ }),
		WithExtractor(resyncExtractorFunc(func(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
			f.extractions++
			target := graph.Resource{ClusterID: f.cluster, Namespace: "test", Kind: "ConfigMap", Name: "edge-recovery"}
			return []graph.Edge{{From: r.ID(), To: target.ID(), Type: graph.EdgeTypeEnforces}}, nil
		})))
	m.coverageSession = f.coverage
	if history {
		cancellationHistory(m, f.store, f.cluster, f.sink)
	}
	return m
}

func TestCoreReplayPreservesDynamicWriterAcrossRestart(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, lifecycle := range []string{"registered", "retired", "replaced", "unknown"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/%s", cluster, history, lifecycle), func(t *testing.T) {
					f := newCoreReceiptFixture(t, cluster, lifecycle)
					m := f.manager(history)
					m.handleUpsert(context.Background(), coreReceiptGVR, f.object, graph.EventTypeAdd)
					reads := f.store.reads
					m.handleUpsert(context.Background(), coreReceiptGVR, f.object.DeepCopy(), graph.EventTypeUpdate)
					wantReads := 0
					if history {
						wantReads = 1
					}
					if f.store.reads-reads != wantReads {
						t.Fatal("unchanged core replay lost its receipt-cache bound", f.store.reads-reads)
					}
					if err := f.store.DeleteEdge(context.Background(), f.stored.ID(), (graph.Resource{ClusterID: cluster, Namespace: "test", Kind: "ConfigMap", Name: "edge-recovery"}).ID(), graph.EdgeTypeEnforces); err != nil {
						t.Fatal(err)
					}
					f.manager(history).handleUpsert(context.Background(), coreReceiptGVR, f.object, graph.EventTypeAdd)
					stored, err := f.store.Store.GetResource(context.Background(), f.stored.ID())
					capture, captureErr := f.tracker.SnapshotCoverage(context.Background(), cluster, nil)
					if err != nil || captureErr != nil || !graph.SameResourceVersion(stored, f.stored) || graph.OriginQualification(stored, capture.Origins) != lifecycle {
						t.Fatal("core replay erased or adopted the dynamic writer", stored, err, captureErr)
					}
					edges, err := f.store.ListEdges(context.Background(), f.stored.ID(), graph.DirectionOutgoing)
					if err != nil || len(edges) != 1 || f.extractions != 3 || f.notifications != 3 || f.store.reads != 2+wantReads || f.store.writes != 0 || len(f.sink.recorded()) != 0 {
						t.Fatal("receipt replay skipped edge recovery or fabricated resource changes", edges, f.store.writes, f.sink.recorded(), err)
					}
					foreign, err := f.store.Store.GetResource(context.Background(), "west:/ClusterPolicy/retained")
					if err != nil || foreign.ResourceVersion != f.stored.ResourceVersion || foreign.Origin.Registration != f.stored.Origin.Registration {
						t.Fatal("core replay changed another member's receipt", foreign, err)
					}
				})
			}
		}
	}
}

func TestCoreReceiptFailureRetainsDataAndAllowsLaterDelivery(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, failure := range []string{"read", "write", "canceled-read"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/%s", cluster, history, failure), func(t *testing.T) {
					f := newCoreReceiptFixture(t, cluster, "retired")
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					prior := time.Now().Add(-time.Minute)
					f.coverage.RecordGap(coreReceiptGVR, operations.CoverageProcessingFailed, prior)
					f.store.readFail, f.store.writeFail = failure == "read", failure == "write"
					if failure == "canceled-read" {
						f.store.onRead = cancel
					}
					object := f.object.DeepCopy()
					object.SetResourceVersion("opaque-next")
					m := f.manager(history)
					m.handleUpsert(ctx, coreReceiptGVR, object, graph.EventTypeUpdate)
					stored, err := f.store.Store.GetResource(context.Background(), f.stored.ID())
					row := coverageView(t, f.tracker, cluster, coreReceiptGVR)
					wantWrites := 0
					if failure == "write" {
						wantWrites = 1
					}
					if err != nil || !graph.SameResourceVersion(stored, f.stored) || f.store.reads != 1 || f.store.writes != wantWrites || f.extractions != 0 || f.notifications != 0 || len(f.sink.recorded()) != 0 {
						t.Fatal("receipt failure fell back to a write/extraction or changed history", stored, f.store.reads, f.store.writes, f.sink.recorded(), err)
					}
					gotGap, wantGap := !row.PersistenceGapSince.IsZero(), failure != "canceled-read"
					if gotGap != wantGap || !row.ProcessingGapSince.Equal(prior) || !row.ExtractionGapSince.IsZero() {
						t.Fatal("receipt failure/cancellation misclassified coverage", row)
					}
					f.store.readFail, f.store.writeFail, f.store.onRead = false, false, nil
					m.handleUpsert(context.Background(), coreReceiptGVR, object, graph.EventTypeUpdate)
					stored, err = f.store.Store.GetResource(context.Background(), f.stored.ID())
					if err != nil || stored.ResourceVersion != "opaque-next" || stored.Origin != (graph.ResourceOrigin{}) || len(f.sink.recorded()) != 1 || f.notifications != 1 {
						t.Fatal("failed lookup suppressed later fresh observation", stored, f.sink.recorded(), err)
					}
					after := coverageView(t, f.tracker, cluster, coreReceiptGVR)
					if !after.PersistenceGapSince.Equal(row.PersistenceGapSince) || !after.ProcessingGapSince.Equal(prior) {
						t.Fatal("later delivery erased earlier loss evidence", after)
					}
				})
			}
		}
	}
}

func TestCoreFreshIdentityOrVersionCanReplaceRetainedWriter(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, change := range []string{"uid", "resource-version"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/%s", cluster, history, change), func(t *testing.T) {
					f := newCoreReceiptFixture(t, cluster, "retired")
					object, event := f.object.DeepCopy(), graph.EventTypeUpdate
					if change == "uid" {
						object.SetUID("replacement-object")
						event = graph.EventTypeAdd
					} else {
						object.SetResourceVersion("opaque-next")
					}
					m := f.manager(history)
					m.handleUpsert(context.Background(), coreReceiptGVR, object, event)
					m.handleUpsert(context.Background(), coreReceiptGVR, object, graph.EventTypeUpdate)
					stored, err := f.store.Store.GetResource(context.Background(), f.stored.ID())
					if err != nil || stored.UID != object.GetUID() || stored.ResourceVersion != object.GetResourceVersion() || stored.Origin != (graph.ResourceOrigin{}) || f.store.writes != 1 || len(f.sink.recorded()) != 1 {
						t.Fatal("fresh core delivery lost its own committed receipt", stored, f.sink.recorded(), err)
					}
				})
			}
		}
	}
}
