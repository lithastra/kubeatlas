// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

var repairPods = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

func repairRow(name string) graph.Resource {
	return graph.Resource{ClusterID: "east", Kind: "Pod", Namespace: "default", Name: name, GroupVersion: "v1", UID: "old-uid", ResourceVersion: "7"}
}

func repairPage(rows ...graph.Resource) *unstructured.UnstructuredList {
	l := &unstructured.UnstructuredList{}
	l.SetResourceVersion("snapshot-10")
	for _, r := range rows {
		u := &unstructured.Unstructured{}
		u.SetKind(r.Kind)
		u.SetAPIVersion(r.GroupVersion)
		u.SetNamespace(r.Namespace)
		u.SetName(r.Name)
		u.SetUID(r.UID)
		u.SetResourceVersion(r.ResourceVersion)
		l.Items = append(l.Items, *u)
	}
	return l
}

func repairClient(reply clienttesting.ReactionFunc) *dynamicfake.FakeDynamicClient {
	c := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{repairPods: "PodList"})
	c.PrependReactor("list", "*", reply)
	return c
}

func repairEvidence(t *testing.T) (*operations.CoverageTracker, *operations.CoverageSession) {
	t.Helper()
	tracker := operations.NewCoverageTracker(time.Minute)
	s, err := tracker.Begin("east", []schema.GroupVersionResource{repairPods}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.InitialDeliveryComplete(repairPods)
	s.ListResult(repairPods, true, false, time.Now())
	s.WatchStarted(repairPods)
	return tracker, s
}

func assertRepairReason(t *testing.T, tracker *operations.CoverageTracker, want operations.CoverageReason) {
	t.Helper()
	s, err := tracker.SnapshotCoverage(context.Background(), "east", []schema.GroupVersionResource{repairPods})
	if err != nil || len(s.Resources) != 1 || !slices.Contains(s.Resources[0].Reasons, want) {
		t.Fatalf("missing repair gap %s: %+v err=%v", want, s, err)
	}
}

func TestMemberRepairScopesRetainedRowsAndCascadesOnlyMissing(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	missing, live := repairRow("gone"), repairRow("live")
	west, standalone, otherVersion, unversioned := missing, missing, missing, missing
	west.ClusterID = "west"
	standalone.ClusterID = ""
	otherVersion.Name = "old-api"
	otherVersion.GroupVersion = "other/v1"
	unversioned.Name = "unversioned"
	unversioned.UID = ""
	for _, row := range []graph.Resource{missing, live, west, standalone, otherVersion, unversioned} {
		if err := s.UpsertResource(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	for _, from := range []graph.Resource{missing, west} {
		if err := s.UpsertEdge(ctx, graph.Edge{From: from.ID(), To: live.ID(), Type: graph.EdgeTypeOwns}); err != nil {
			t.Fatal(err)
		}
	}
	// Same-name presence protects a replacement even before its watch write.
	live.UID = "new-uid"
	c := repairClient(func(a clienttesting.Action) (bool, runtime.Object, error) {
		opts := a.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
		if opts.ResourceVersion != "" || opts.LabelSelector != "" || opts.FieldSelector != "" {
			t.Errorf("unsafe list options: %+v", opts)
		}
		return true, repairPage(live), nil
	})
	tracker, coverage := repairEvidence(t)
	r := NewMemberRepair(c, s, "east")
	var removed []string
	for range 2 {
		r.repairOnce(ctx, repairPods, "Pod", coverage, func(row graph.Resource) { removed = append(removed, row.ID()) })
	}
	if !slices.Equal(removed, []string{missing.ID()}) {
		t.Fatalf("wrong committed deletions: %v", removed)
	}
	for _, row := range []graph.Resource{live, west, standalone, otherVersion, unversioned} {
		if _, err := s.GetResource(ctx, row.ID()); err != nil {
			t.Fatalf("unowned/present row removed: %s", row.ID())
		}
	}
	edges, err := s.ListEdges(ctx, live.ID(), graph.DirectionIncoming)
	if err != nil || len(edges) != 1 || edges[0].From != west.ID() {
		t.Fatalf("wrong cascading deletion: %+v %v", edges, err)
	}
	assertRepairReason(t, tracker, operations.CoverageProcessingFailed) // unversioned row retained
}

func TestMemberRepairCompletesFreshPaginatedSnapshot(t *testing.T) {
	s := memory.New()
	missing, first, second := repairRow("gone"), repairRow("first"), repairRow("second")
	for _, row := range []graph.Resource{missing, first, second} {
		_ = s.UpsertResource(context.Background(), row)
	}
	calls := 0
	// A direct stub preserves every ListOption. client-go's root-list fake
	// discards Limit/Continue, so it cannot prove pagination forwarding.
	c := &coverageStubClient{resource: &coverageStubResource{list: func(_ context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
		calls++
		if opts.ResourceVersion != "" || opts.ResourceVersionMatch != "" || opts.Limit != memberRepairPageSize || opts.LabelSelector != "" || opts.FieldSelector != "" {
			t.Errorf("unsafe fresh-list options: %+v", opts)
		}
		if calls == 1 {
			if opts.Continue != "" {
				t.Fatal("first request had a continue token")
			}
			l := repairPage(first)
			l.SetContinue("opaque-next-page")
			return l, nil
		}
		if opts.Continue != "opaque-next-page" {
			t.Fatal("pagination token not forwarded")
		}
		return repairPage(second), nil
	}}}
	NewMemberRepair(c, s, "east").repairOnce(context.Background(), repairPods, "Pod", nil, nil)
	if calls != 2 {
		t.Fatalf("list calls=%d", calls)
	}
	for _, row := range []graph.Resource{first, second} {
		if _, err := s.GetResource(context.Background(), row.ID()); err != nil {
			t.Fatal("partial page used as full absence evidence")
		}
	}
	var absent graph.ErrNotFound
	_, err := s.GetResource(context.Background(), missing.ID())
	if !errors.As(err, &absent) {
		t.Fatal("complete snapshot did not repair missing row")
	}
}

func TestMemberRepairDoesNotSweepIncompleteOrInvalidLists(t *testing.T) {
	for _, scenario := range []string{"denied", "partial-page", "changed-snapshot", "repeated-token", "missing-snapshot", "invalid-item", "duplicate-item", "limit", "remaining-without-token", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			s := memory.New()
			row := repairRow("gone")
			_ = s.UpsertResource(context.Background(), row)
			tracker, coverage := repairEvidence(t)
			calls := 0
			c := repairClient(func(a clienttesting.Action) (bool, runtime.Object, error) {
				calls++
				l := repairPage()
				switch scenario {
				case "denied":
					return true, nil, apierrors.NewForbidden(repairPods.GroupResource(), "", errors.New("private-detail"))
				case "partial-page":
					if calls == 2 {
						return true, nil, apierrors.NewResourceExpired("expired-page")
					}
					l.SetContinue("next")
				case "changed-snapshot":
					if calls == 1 {
						l.SetContinue("next")
					} else {
						l.SetResourceVersion("different")
					}
				case "repeated-token":
					l.SetContinue("same-token")
				case "missing-snapshot":
					l.SetResourceVersion("")
				case "remaining-without-token":
					remaining := int64(1)
					l.SetRemainingItemCount(&remaining)
				case "invalid-item":
					bad := repairRow("bad")
					bad.UID = ""
					l = repairPage(bad)
				case "duplicate-item":
					l = repairPage(repairRow("duplicate"), repairRow("duplicate"))
				case "limit":
					for i := 0; i <= memberRepairLimit; i++ {
						item := repairRow(time.Unix(int64(i), 0).Format(time.RFC3339))
						l.Items = append(l.Items, repairPage(item).Items[0])
					}
				}
				return true, l, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancelled" {
				cancel()
			}
			NewMemberRepair(c, s, "east").repairOnce(ctx, repairPods, "Pod", coverage, nil)
			if _, err := s.GetResource(context.Background(), row.ID()); err != nil {
				t.Fatal("incomplete list authorized deletion")
			}
			if scenario == "cancelled" {
				if calls != 0 {
					t.Fatal("stopped registration requested list")
				}
			} else {
				assertRepairReason(t, tracker, operations.CoverageProcessingFailed)
			}
		})
	}
}

func TestMemberRepairScheduledRetryAndCoreDeletionReceipt(t *testing.T) {
	s := &repairFailStore{Store: memory.New()}
	s.failures.Store(10000)
	s.normalFailures.Store(1)
	row := repairRow("gone")
	_ = s.UpsertResource(context.Background(), row)
	tracker := operations.NewCoverageTracker(time.Minute)
	u := repairPage(row).Items[0]
	c := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{repairPods: "PodList"}, &u)
	r := NewMemberRepair(c, s, "east")
	r.interval = 10 * time.Millisecond
	sink := &resyncSink{}
	var notifications atomic.Int32
	m := NewInformerManager(c, s, WithGVRs([]schema.GroupVersionResource{repairPods}), WithClusterID("east"), WithCoverage(tracker), WithMemberRepair(r), WithSnapshotSink(sink), WithBroadcaster(func(op, _, _, _ string) {
		if op == "delete" {
			notifications.Add(1)
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("core repair was not joined")
		}
	})
	awaitCoverage(t, func() bool {
		return len(sink.recorded()) == 1 && coverageView(t, tracker, "east", repairPods).State == operations.CoverageObserved
	})
	if err := c.Resource(repairPods).Namespace(row.Namespace).Delete(ctx, row.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitCoverage(t, func() bool {
		return slices.Contains(coverageView(t, tracker, "east", repairPods).Reasons, operations.CoveragePersistenceFailed)
	})
	if len(sink.recorded()) != 1 || notifications.Load() != 0 {
		t.Fatal("failed watch deletion emitted a success receipt")
	}
	if _, err := s.GetResource(ctx, row.ID()); err != nil {
		t.Fatal("failed watch deletion unexpectedly removed row")
	}
	s.failures.Store(0)
	awaitCoverage(t, func() bool { return len(sink.recorded()) == 2 && notifications.Load() == 1 })
	events := sink.recorded()
	if events[1].EventType != graph.EventTypeDelete || events[1].ClusterID != "east" || events[1].UID != string(row.UID) || events[1].ResourceVersion != row.ResourceVersion || notifications.Load() != 1 {
		t.Fatalf("invalid committed repair receipt: %+v", events)
	}
	assertRepairReason(t, tracker, operations.CoveragePersistenceFailed)
	var absent graph.ErrNotFound
	_, err := s.GetResource(context.Background(), row.ID())
	if !errors.As(err, &absent) {
		t.Fatal("scheduled retry left missing row")
	}
}

func TestMemberRepairCanonicalCoreKinds(t *testing.T) {
	for _, gvr := range CoreGVRs {
		if coreRepairKind(gvr) == "" {
			t.Errorf("missing repair kind for %s", gvr)
		}
	}
	if coreRepairKind(schema.GroupVersionResource{Group: "unknown", Version: "v1", Resource: "pods"}) != "" {
		t.Fatal("custom plural guessed as core kind")
	}
}

func TestMemberRepairJoinsObjectPipelineBeforeCascadingEdges(t *testing.T) {
	testRepairJoinsObjectPipeline(t, false)
}

func TestStandaloneRepairJoinsObjectPipelineBeforeCascadingEdges(t *testing.T) {
	testRepairJoinsObjectPipeline(t, true)
}

func testRepairJoinsObjectPipeline(t *testing.T, standalone bool) {
	t.Helper()
	s := memory.New()
	row, target := repairRow("gone"), repairRow("target")
	if standalone {
		row.ClusterID, target.ClusterID = "", ""
	}
	_ = s.UpsertResource(context.Background(), target)
	listed := make(chan struct{})
	c := repairClient(func(clienttesting.Action) (bool, runtime.Object, error) {
		close(listed)
		return true, repairPage(target), nil
	})
	r := NewMemberRepair(c, s, "east")
	if standalone {
		r = NewStandaloneRepair(c, s)
	}
	entered, release, callbackDone, repairDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	m := NewInformerManager(c, s, WithClusterID(row.ClusterID), WithMemberRepair(r), WithExtractor(resyncExtractorFunc(func(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error) {
		close(entered)
		<-release
		return []graph.Edge{{From: row.ID(), To: target.ID(), Type: graph.EdgeTypeOwns}}, nil
	})))
	u := repairPage(row).Items[0]
	go func() {
		defer close(callbackDone)
		m.handleUpsert(context.Background(), repairPods, &u, graph.EventTypeAdd)
	}()
	<-entered
	go func() { defer close(repairDone); r.repairOnce(context.Background(), repairPods, "Pod", nil, nil) }()
	<-listed
	select {
	case <-repairDone:
		t.Fatal("repair did not wait for complete object pipeline")
	case <-time.After(30 * time.Millisecond):
	}
	// A blocked object does not serialize unrelated callbacks.
	unlock, err := r.LockResource(context.Background(), target.ID())
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	close(release)
	<-callbackDone
	<-repairDone
	edges, err := s.ListEdges(context.Background(), target.ID(), graph.DirectionIncoming)
	if err != nil || len(edges) != 0 {
		t.Fatal("extractor wrote incident edges after repair deletion")
	}
	r.mu.Lock()
	locks := len(r.writes)
	r.mu.Unlock()
	if locks != 0 {
		t.Fatal("object delivery locks retained after last owner")
	}
}

func TestMemberRepairBoundsConcurrentRoundsAndCancelsWaiters(t *testing.T) {
	s := memory.New()
	_ = s.UpsertResource(context.Background(), repairRow("gone"))
	entered, release, firstDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	c := &coverageStubClient{resource: &coverageStubResource{list: func(context.Context, metav1.ListOptions) (*unstructured.UnstructuredList, error) {
		if calls.Add(1) != 1 {
			t.Error("concurrent repair list escaped member bound")
		}
		close(entered)
		<-release
		return repairPage(), nil
	}}}
	r := NewMemberRepair(c, s, "east")
	go func() { defer close(firstDone); r.repairOnce(context.Background(), repairPods, "Pod", nil, nil) }()
	<-entered
	tracker, coverage := repairEvidence(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r.repairOnce(ctx, repairPods, "Pod", coverage, nil)
	if calls.Load() != 1 {
		t.Fatal("timed-out waiter issued a request")
	}
	assertRepairReason(t, tracker, operations.CoverageProcessingFailed)
	close(release)
	<-firstDone
	unlock, err := r.LockResource(context.Background(), "same-object")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancelWaiter := context.WithCancel(context.Background())
	cancelWaiter()
	if release, err := r.LockResource(ctx, "same-object"); err == nil {
		release()
		t.Fatal("cancelled object waiter acquired lock")
	}
	unlock()
	r.mu.Lock()
	locks := len(r.writes)
	r.mu.Unlock()
	if locks != 0 {
		t.Fatal("cancelled waiter leaked its object lock")
	}
}

func TestMemberRepairPreservesRowsChangedDuringListing(t *testing.T) {
	for _, change := range []string{"uid", "resourceVersion", "apiVersion"} {
		t.Run(change, func(t *testing.T) {
			s := memory.New()
			old := repairRow("same")
			_ = s.UpsertResource(context.Background(), old)
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			c := repairClient(func(clienttesting.Action) (bool, runtime.Object, error) {
				close(entered)
				<-release
				return true, repairPage(), nil
			})
			go func() {
				defer close(done)
				NewMemberRepair(c, s, "east").repairOnce(context.Background(), repairPods, "Pod", nil, nil)
			}()
			<-entered
			fresh := old
			switch change {
			case "uid":
				fresh.UID = "replacement"
			case "resourceVersion":
				fresh.ResourceVersion = "8"
			case "apiVersion":
				fresh.GroupVersion = "other/v1"
			}
			if err := s.UpsertResource(context.Background(), fresh); err != nil {
				t.Fatal(err)
			}
			close(release)
			<-done
			got, err := s.GetResource(context.Background(), fresh.ID())
			if err != nil || !graph.SameResourceVersion(got, fresh) {
				t.Fatal("in-flight list removed newer observation")
			}
		})
	}
}

type repairFailStore struct {
	*memory.Store
	failures       atomic.Int32
	normalFailures atomic.Int32
}

func (s *repairFailStore) DeleteResource(ctx context.Context, id string) error {
	if s.normalFailures.Add(-1) >= 0 {
		return errors.New("synthetic-watch-delete-failure")
	}
	return s.Store.DeleteResource(ctx, id)
}

func (s *repairFailStore) DeleteResourceVersion(ctx context.Context, r graph.Resource) (bool, error) {
	if s.failures.Add(-1) >= 0 {
		return false, errors.New("synthetic-delete-failure")
	}
	return s.Store.DeleteResourceVersion(ctx, r)
}

func TestMemberRepairRetriesFailedDeleteWithoutResettingGap(t *testing.T) {
	s := &repairFailStore{Store: memory.New()}
	s.failures.Store(1)
	row := repairRow("gone")
	_ = s.UpsertResource(context.Background(), row)
	tracker, coverage := repairEvidence(t)
	c := repairClient(func(clienttesting.Action) (bool, runtime.Object, error) { return true, repairPage(), nil })
	r := NewMemberRepair(c, s, "east")
	notified := 0
	notify := func(graph.Resource) { notified++ }
	r.repairOnce(context.Background(), repairPods, "Pod", coverage, notify)
	if _, err := s.GetResource(context.Background(), row.ID()); err != nil || notified != 0 {
		t.Fatal("failed deletion was reported committed")
	}
	r.repairOnce(context.Background(), repairPods, "Pod", coverage, notify)
	var absent graph.ErrNotFound
	_, err := s.GetResource(context.Background(), row.ID())
	if !errors.As(err, &absent) || notified != 1 {
		t.Fatal("next full round did not repair failed delete")
	}
	assertRepairReason(t, tracker, operations.CoveragePersistenceFailed)
}

func TestMemberRepairRequiresAtomicBackendAndNeverRequestsSecrets(t *testing.T) {
	s := memory.New()
	_ = s.UpsertResource(context.Background(), repairRow("gone"))
	c := repairClient(func(clienttesting.Action) (bool, runtime.Object, error) {
		t.Error("unsafe repair made a request")
		return true, nil, nil
	})
	tracker, coverage := repairEvidence(t)
	// Embedding the GraphStore interface intentionally hides the optional capability.
	wrapped := struct{ graph.GraphStore }{s}
	NewMemberRepair(c, wrapped, "east").repairOnce(context.Background(), repairPods, "Pod", coverage, nil)
	assertRepairReason(t, tracker, operations.CoveragePersistenceFailed)
	NewMemberRepair(c, s, "east").repairOnce(context.Background(), schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, "Secret", nil, nil)
	NewMemberRepair(c, s, "").repairOnce(context.Background(), repairPods, "Pod", nil, nil)
	if len(c.Actions()) != 0 {
		t.Fatal("unowned repair or Secret request")
	}
}

func TestMemberRepairRunWaitsForSyncAndStopsWithRegistration(t *testing.T) {
	testRepairRunLifecycle(t, false)
}

func TestStandaloneRepairRunWaitsForSyncAndStopsWithRegistration(t *testing.T) {
	testRepairRunLifecycle(t, true)
}

func testRepairRunLifecycle(t *testing.T, standalone bool) {
	t.Helper()
	s := memory.New()
	row := repairRow("gone")
	if standalone {
		row.ClusterID = ""
	}
	_ = s.UpsertResource(context.Background(), row)
	var synced atomic.Bool
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	c := repairClient(func(clienttesting.Action) (bool, runtime.Object, error) {
		close(entered)
		<-release
		return true, repairPage(), nil
	})
	r := NewMemberRepair(c, s, "east")
	if standalone {
		r = NewStandaloneRepair(c, s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { defer close(done); r.Run(ctx, repairPods, "Pod", nil, synced.Load, nil) }()
	select {
	case <-entered:
		t.Fatal("repair ran before initial delivery")
	case <-time.After(30 * time.Millisecond):
	}
	synced.Store(true)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("repair did not run after delivery")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("worker returned before list completed")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("repair failed to stop")
	}
	if _, err := s.GetResource(context.Background(), row.ID()); err != nil {
		t.Fatal("cancelled round removed retained row")
	}
}
