// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

type cacheHandoffResult struct {
	value CacheValue
	err   error
}

// Done is consulted on each interruptible wait. This lets the tests acknowledge
// both the original flight and the replacement flight without scheduler sleeps.
type cacheWaitContext struct {
	context.Context
	waiting chan struct{}
}

func (c cacheWaitContext) Done() <-chan struct{} {
	select {
	case c.waiting <- struct{}{}:
	default:
	}
	return c.Context.Done()
}

func awaitCacheSignal[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("cache handoff did not reach its next phase")
		var zero T
		return zero
	}
}

func cacheHandoffValue(target string) CacheValue {
	return CacheValue{Edges: []graph.Edge{{From: "default/Foo/source", To: target, Type: graph.EdgeTypeOwns}}}
}

func assertCacheFlightCount(t *testing.T, cache *Cache, want int) {
	t.Helper()
	cache.mu.Lock()
	got := len(cache.inflight)
	cache.mu.Unlock()
	if got != want {
		t.Fatalf("in-flight evaluations=%d, want %d", got, want)
	}
}

func TestCacheHandoffCancelledCaller(t *testing.T) {
	for _, state := range []string{"cold", "warm", "waiting"} {
		for _, reason := range []string{"cancel", "deadline"} {
			t.Run(state+"/"+reason, func(t *testing.T) {
				cache, err := NewCache(4, nil)
				if err != nil {
					t.Fatal(err)
				}
				key := CacheKey{ClusterID: "east", UID: "uid", ResourceVersion: "version", RuleHash: "rule"}
				value := cacheHandoffValue("east:default/Bar/target")
				leader := make(chan cacheHandoffResult, 1)
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				releaseLeader := func() { once.Do(func() { close(release) }) }
				defer releaseLeader()
				switch state {
				case "warm":
					if _, err := cache.GetOrEvaluate(context.Background(), key, func(context.Context) (CacheValue, error) { return value, nil }); err != nil {
						t.Fatal(err)
					}
				case "waiting":
					go func() {
						v, err := cache.GetOrEvaluate(context.Background(), key, func(context.Context) (CacheValue, error) {
							close(entered)
							<-release
							return value, nil
						})
						leader <- cacheHandoffResult{v, err}
					}()
					awaitCacheSignal(t, entered)
				}
				before := cache.metrics.Snapshot()
				ctx, cancel := context.WithCancel(context.Background())
				wantErr := context.Canceled
				if reason == "deadline" {
					cancel()
					deadline := time.Now().Add(-time.Second)
					if state == "waiting" {
						deadline = time.Now().Add(20 * time.Millisecond)
					}
					ctx, cancel = context.WithDeadline(context.Background(), deadline)
					wantErr = context.DeadlineExceeded
				} else if state != "waiting" {
					cancel()
				}
				defer cancel()
				callCtx := ctx
				waiting := make(chan struct{}, 2)
				if state == "waiting" {
					callCtx = cacheWaitContext{ctx, waiting}
				}
				var calls atomic.Int32
				result := make(chan cacheHandoffResult, 1)
				go func() {
					v, err := cache.GetOrEvaluate(callCtx, key, func(context.Context) (CacheValue, error) {
						calls.Add(1)
						return value, nil
					})
					result <- cacheHandoffResult{v, err}
				}()
				if state == "waiting" && reason == "cancel" {
					awaitCacheSignal(t, waiting)
					cancel()
				}
				got := awaitCacheSignal(t, result) // Must return before releasing the leader.
				if !errors.Is(got.err, wantErr) || len(got.value.Edges) != 0 || calls.Load() != 0 || cache.metrics.Snapshot() != before {
					t.Fatal("cancelled caller consumed or began evaluation", got, calls.Load(), cache.metrics.Snapshot())
				}
				if state == "waiting" {
					assertCacheFlightCount(t, cache, 1)
					releaseLeader()
					got = awaitCacheSignal(t, leader)
					if got.err != nil || len(got.value.Edges) != 1 || got.value.Edges[0].To != value.Edges[0].To {
						t.Fatal("cancelled waiter disturbed its leader", got)
					}
				}
				assertCacheFlightCount(t, cache, 0)
			})
		}
	}
}

func TestCacheFailedLeaderHandsOffOneEvaluator(t *testing.T) {
	for _, failure := range []string{"error", "canceled-error", "canceled-success"} {
		for _, followers := range []int{1, 8} {
			t.Run(fmt.Sprintf("%s/followers=%d", failure, followers), func(t *testing.T) {
				cache, err := NewCache(4, nil)
				if err != nil {
					t.Fatal(err)
				}
				key := CacheKey{ClusterID: "east", UID: "uid", ResourceVersion: "version", RuleHash: "rule"}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				entered, release, replacementRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var firstOnce, replacementOnce sync.Once
				releaseFirst := func() { firstOnce.Do(func() { close(release) }) }
				releaseReplacement := func() { replacementOnce.Do(func() { close(replacementRelease) }) }
				defer releaseFirst()
				defer releaseReplacement()
				leader := make(chan cacheHandoffResult, 1)
				leaderErr := errors.New("leader evaluation failed")
				go func() {
					v, err := cache.GetOrEvaluate(ctx, key, func(context.Context) (CacheValue, error) {
						close(entered)
						<-release
						if failure == "canceled-success" {
							return cacheHandoffValue("late-cancelled-value"), nil
						}
						return cacheHandoffValue("failed-value"), leaderErr
					})
					leader <- cacheHandoffResult{v, err}
				}()
				awaitCacheSignal(t, entered)
				waits := make([]chan struct{}, followers)
				results := make(chan cacheHandoffResult, followers)
				replacement := make(chan int, followers)
				var evaluations atomic.Int32
				for i := range followers {
					waits[i] = make(chan struct{}, 2)
					waitCtx := cacheWaitContext{context.Background(), waits[i]}
					go func(index int) {
						v, err := cache.GetOrEvaluate(waitCtx, key, func(context.Context) (CacheValue, error) {
							evaluations.Add(1)
							replacement <- index
							<-replacementRelease
							return cacheHandoffValue("recovered-value"), nil
						})
						results <- cacheHandoffResult{v, err}
					}(i)
				}
				for _, wait := range waits {
					awaitCacheSignal(t, wait)
				}
				if failure != "error" {
					cancel()
				}
				releaseFirst()
				got := awaitCacheSignal(t, leader)
				wantErr := leaderErr
				if failure != "error" {
					wantErr = context.Canceled
				}
				if !errors.Is(got.err, wantErr) || len(got.value.Edges) != 0 {
					t.Fatal("failed/cancelled leader published a value", got)
				}
				winner := awaitCacheSignal(t, replacement)
				for i, wait := range waits {
					if i != winner {
						awaitCacheSignal(t, wait) // All others now wait on the replacement.
					}
				}
				if evaluations.Load() != 1 || cache.Len() != 0 {
					t.Fatal("failure handoff duplicated work or cached a failed value", evaluations.Load(), cache.Len())
				}
				assertCacheFlightCount(t, cache, 1)
				releaseReplacement()
				for range followers {
					got := awaitCacheSignal(t, results)
					if got.err != nil || len(got.value.Edges) != 1 || got.value.Edges[0].To != "recovered-value" {
						t.Fatal("waiter did not consume the recovered value", got)
					}
				}
				metrics := cache.metrics.Snapshot()
				if evaluations.Load() != 1 || metrics.CacheMisses != 2 || metrics.CacheHits != uint64(followers-1) || cache.Len() != 1 {
					t.Fatal("handoff broke deduplication or accounting", evaluations.Load(), metrics, cache.Len())
				}
				assertCacheFlightCount(t, cache, 0)
			})
		}
	}
}

func TestCacheFailedFollowersReturnTheirOwnErrors(t *testing.T) {
	cache, err := NewCache(4, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := CacheKey{UID: "uid", ResourceVersion: "version", RuleHash: "rule"}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseLeader := func() { once.Do(func() { close(release) }) }
	defer releaseLeader()
	leader := make(chan cacheHandoffResult, 1)
	initialErr := errors.New("initial evaluation failed")
	go func() {
		v, err := cache.GetOrEvaluate(context.Background(), key, func(context.Context) (CacheValue, error) {
			close(entered)
			<-release
			return CacheValue{}, initialErr
		})
		leader <- cacheHandoffResult{v, err}
	}()
	awaitCacheSignal(t, entered)
	const followers = 8
	waits, results := make([]chan struct{}, followers), make([]chan cacheHandoffResult, followers)
	errorsByCaller := make([]error, followers)
	var calls atomic.Int32
	for i := range followers {
		waits[i], results[i], errorsByCaller[i] = make(chan struct{}, 2), make(chan cacheHandoffResult, 1), fmt.Errorf("caller %d failed", i)
		waitCtx := cacheWaitContext{context.Background(), waits[i]}
		go func(index int) {
			v, err := cache.GetOrEvaluate(waitCtx, key, func(context.Context) (CacheValue, error) {
				calls.Add(1)
				return cacheHandoffValue("failed-partial"), errorsByCaller[index]
			})
			results[index] <- cacheHandoffResult{v, err}
		}(i)
	}
	for _, wait := range waits {
		awaitCacheSignal(t, wait)
	}
	releaseLeader()
	if got := awaitCacheSignal(t, leader); !errors.Is(got.err, initialErr) {
		t.Fatal("leader lost its own failure", got)
	}
	for i, result := range results {
		got := awaitCacheSignal(t, result)
		if !errors.Is(got.err, errorsByCaller[i]) || len(got.value.Edges) != 0 {
			t.Fatal("waiter inherited another caller's failure/value", i, got)
		}
	}
	if calls.Load() != followers || cache.Len() != 0 || cache.metrics.Snapshot().CacheMisses != followers+1 {
		t.Fatal("failed calls retried internally or poisoned the cache", calls.Load(), cache.metrics.Snapshot())
	}
	assertCacheFlightCount(t, cache, 0)
}

func TestCacheHandoffDoesNotSerializeOtherMembers(t *testing.T) {
	cache, err := NewCache(4, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := CacheKey{ClusterID: "east", UID: "same-uid", ResourceVersion: "same-version", RuleHash: "same-rule"}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseLeader := func() { once.Do(func() { close(release) }) }
	defer releaseLeader()
	leader := make(chan cacheHandoffResult, 1)
	go func() {
		v, err := cache.GetOrEvaluate(context.Background(), key, func(context.Context) (CacheValue, error) {
			close(entered)
			<-release
			return cacheHandoffValue("east-value"), nil
		})
		leader <- cacheHandoffResult{v, err}
	}()
	awaitCacheSignal(t, entered)
	other := key
	other.ClusterID = "west"
	result := make(chan cacheHandoffResult, 1)
	go func() {
		v, err := cache.GetOrEvaluate(context.Background(), other, func(context.Context) (CacheValue, error) { return cacheHandoffValue("west-value"), nil })
		result <- cacheHandoffResult{v, err}
	}()
	got := awaitCacheSignal(t, result) // The unrelated key must finish while east is blocked.
	if got.err != nil || len(got.value.Edges) != 1 || got.value.Edges[0].To != "west-value" {
		t.Fatal("another member inherited the blocked evaluation", got)
	}
	releaseLeader()
	got = awaitCacheSignal(t, leader)
	if got.err != nil || len(got.value.Edges) != 1 || got.value.Edges[0].To != "east-value" || cache.Len() != 2 {
		t.Fatal("member evaluations shared cache values", got, cache.Len())
	}
	assertCacheFlightCount(t, cache, 0)
}

func TestEngineCancelledWarmReplayDoesNotConsumeCache(t *testing.T) {
	engine, metrics := newWiredEngine(t)
	r := fooResource(1, "version")
	if _, err := engine.EvaluateForResource(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	before := metrics.Snapshot()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	edges, err := engine.EvaluateForResource(ctx, r)
	if !errors.Is(err, context.Canceled) || len(edges) != 0 || metrics.Snapshot() != before {
		t.Fatal("cancelled engine replay consumed a cached result", edges, err, metrics.Snapshot())
	}
}
