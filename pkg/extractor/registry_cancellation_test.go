// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

type registryStep func(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error)

func (registryStep) Type() graph.EdgeType { return graph.EdgeTypeOwns }
func (f registryStep) Extract(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
	return f(ctx, r, q)
}

func TestRegistryCancellationDiscardsBatchAndStopsLaterExtractors(t *testing.T) {
	for _, cancelAt := range []int{-1, 0, 1, 2} {
		for _, reply := range []string{"success", "partial-error", "context-error"} {
			t.Run(fmt.Sprintf("cancelAt=%d/%s", cancelAt, reply), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				reg := New()
				var calls []int
				for index := range 3 {
					reg.Register(registryStep(func(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
						calls = append(calls, index)
						var err error
						if index == 0 && cancelAt > 0 {
							err = errors.New("earlier active failure")
						}
						if index == cancelAt {
							cancel()
							switch reply {
							case "partial-error":
								err = errors.New("late partial failure")
							case "context-error":
								err = ctx.Err()
							}
						}
						return []graph.Edge{{From: r.ID(), To: r.ID(), Type: graph.EdgeTypeOwns}}, err
					}))
				}
				if cancelAt == -1 {
					cancel()
				}
				edges, err := reg.ExtractAll(ctx, graph.Resource{Kind: "Pod", Name: "root"}, nil)
				if edges != nil || !errors.Is(err, context.Canceled) || len(calls) != cancelAt+1 {
					t.Fatal("cancelled batch escaped or started another extractor", edges, calls, err)
				}
			})
		}
	}
}

func TestRegistryCancelledEmptyAndExpiredCalls(t *testing.T) {
	for _, mode := range []string{"empty-cancelled", "empty-expired", "nonempty-expired"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if mode != "empty-cancelled" {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				want = context.DeadlineExceeded
			} else {
				cancel()
			}
			defer cancel()
			reg := New()
			if mode == "nonempty-expired" {
				reg.Register(registryStep(func(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error) {
					t.Fatal("expired call entered extractor")
					return nil, nil
				}))
			}
			edges, err := reg.ExtractAll(ctx, graph.Resource{}, nil)
			if edges != nil || !errors.Is(err, want) {
				t.Fatal("empty or expired registry lost parent cancellation", edges, err)
			}
		})
	}
}

func TestRegistryActiveFailuresPreservePartialResultsAndLaterWork(t *testing.T) {
	for _, failure := range []error{errors.New("ordinary failure"), context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			reg := New()
			calls := 0
			want := []graph.Edge{{From: "default/Pod/root", To: "default/Pod/first", Type: graph.EdgeTypeOwns}, {From: "default/Pod/root", To: "default/Pod/last", Type: graph.EdgeTypeOwns}}
			for index := range 2 {
				reg.Register(registryStep(func(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error) {
					calls++
					if index == 0 {
						return want[:1], failure
					}
					return want[1:], errors.New("later failure")
				}))
			}
			got, err := reg.ExtractAll(context.Background(), graph.Resource{}, nil)
			if calls != 2 || !errors.Is(err, failure) || !reflect.DeepEqual(got, want) {
				t.Fatal("dependency error cancelled an active parent or lost partial results", got, calls, err)
			}
		})
	}
}

func TestRegistryBuiltinLateReadCannotAdvanceCancelledBatch(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		t.Run("cluster="+cluster, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			service := graph.Resource{ClusterID: cluster, Kind: "Service", Namespace: "default", Name: "service", Raw: map[string]any{"spec": map[string]any{"selector": map[string]any{"app": "selected"}}}}
			reads, later := 0, 0
			lister := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
				reads++
				cancel() // A dependency already in flight ignores cancellation.
				return []graph.Resource{{ClusterID: cluster, Kind: "Pod", Namespace: "default", Name: "late", Labels: map[string]string{"app": "selected"}}}, nil
			})
			reg := New()
			reg.Register(SelectsExtractor{})
			reg.Register(registryStep(func(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error) {
				later++
				return nil, nil
			}))
			edges, err := reg.ExtractAll(ctx, service, lister)
			if edges != nil || !errors.Is(err, context.Canceled) || reads != 1 || later != 0 {
				t.Fatal("late builtin output escaped or advanced registry", edges, reads, later, err)
			}
		})
	}
}

func TestRegistryCancelledCallDoesNotBlockConcurrentSuccessor(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		t.Run("cluster="+cluster, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			reg := New()
			reg.Register(registryStep(func(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
				if r.Name == "old" {
					close(entered)
					<-release
				}
				return []graph.Edge{{From: r.ID(), To: r.ID(), Type: graph.EdgeTypeOwns}}, nil
			}))
			reg.Register(registryStep(func(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
				if r.Name == "old" {
					t.Error("retired call entered a later extractor")
				}
				return []graph.Edge{{From: r.ID(), To: r.ID(), Type: graph.EdgeTypeSelects}}, nil
			}))
			done := make(chan error, 1)
			go func() {
				edges, err := reg.ExtractAll(ctx, graph.Resource{ClusterID: cluster, Kind: "Pod", Name: "old"}, nil)
				if edges != nil {
					done <- errors.New("retired call returned output")
					return
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("old call did not enter dependency")
			}
			cancel()
			successor := graph.Resource{ClusterID: cluster, Kind: "Pod", Name: "new"}
			edges, err := reg.ExtractAll(context.Background(), successor, nil)
			if err != nil || len(edges) != 2 || edges[0].From != successor.ID() || edges[1].From != successor.ID() {
				t.Fatal("cancelled caller contaminated independent successor", edges, err)
			}
			close(release)
			released = true
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("old call lost cancellation", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("old call did not join after dependency returned")
			}
		})
	}
}
