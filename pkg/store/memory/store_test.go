package memory_test

import (
	"context"
	"sync"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestStore_Contract(t *testing.T) {
	storetest.Run(t, func(_ *testing.T) graph.GraphStore {
		return memory.New()
	})
}

func TestStore_ConcurrentUpserts(t *testing.T) {
	s := memory.New()
	const writers = 32
	const perWriter = 100
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				r := graph.Resource{
					Kind:      "Pod",
					Namespace: "demo",
					Name:      podName(w, i),
				}
				if err := s.UpsertResource(context.Background(), r); err != nil {
					t.Errorf("upsert: %v", err)
				}
			}
		}(w)
	}
	wg.Wait()
	all, err := s.ListResources(context.Background(), graph.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != writers*perWriter {
		t.Errorf("got %d resources, want %d", len(all), writers*perWriter)
	}
}

func TestStore_DeleteResourceLeavesUnrelatedEdgesAlone(t *testing.T) {
	s := memory.New()
	ctx := context.Background()
	_ = s.UpsertResource(ctx, graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "a"})
	_ = s.UpsertResource(ctx, graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "b"})
	_ = s.UpsertResource(ctx, graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "shared"})
	_ = s.UpsertEdge(ctx, graph.Edge{From: "demo/Deployment/a", To: "demo/ConfigMap/shared", Type: graph.EdgeTypeUsesConfigMap})
	_ = s.UpsertEdge(ctx, graph.Edge{From: "demo/Deployment/b", To: "demo/ConfigMap/shared", Type: graph.EdgeTypeUsesConfigMap})

	if err := s.DeleteResource(ctx, "demo/Deployment/a"); err != nil {
		t.Fatal(err)
	}
	in, _ := s.ListIncoming(ctx, "demo/ConfigMap/shared")
	if len(in) != 1 {
		t.Errorf("expected 1 surviving incoming edge, got %d", len(in))
	}
	if len(in) == 1 && in[0].From != "demo/Deployment/b" {
		t.Errorf("wrong edge survived: %+v", in[0])
	}
}

func podName(w, i int) string {
	// Simple hand-rolled formatter so tests don't depend on fmt.
	const digits = "0123456789"
	buf := []byte("pod-")
	for _, n := range []int{w, i} {
		if n == 0 {
			buf = append(buf, '0')
		}
		// Build the digits in reverse, then append.
		var rev []byte
		for n > 0 {
			rev = append(rev, digits[n%10])
			n /= 10
		}
		for j := len(rev) - 1; j >= 0; j-- {
			buf = append(buf, rev[j])
		}
		buf = append(buf, '-')
	}
	return string(buf[:len(buf)-1])
}
