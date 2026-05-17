package extractor

import (
	"context"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// sliceLister is an in-memory graph.ResourceLister backed by a fixed
// slice — the test stand-in for a real GraphStore. It applies the
// same Filter semantics the memory / postgres stores do: an empty
// Kind / Namespace field means "any", and Labels is exact-match
// containment.
type sliceLister []graph.Resource

func (s sliceLister) ListResources(_ context.Context, f graph.Filter) ([]graph.Resource, error) {
	var out []graph.Resource
	for _, r := range s {
		if f.Kind != "" && r.Kind != f.Kind {
			continue
		}
		if f.Namespace != "" && r.Namespace != f.Namespace {
			continue
		}
		match := true
		for k, v := range f.Labels {
			if r.Labels[k] != v {
				match = false
				break
			}
		}
		if match {
			out = append(out, r)
		}
	}
	return out, nil
}

// extractEdges runs one extractor against a slice-backed lister and
// fails the test on error — keeping the extractor tests focused on
// the edges produced, not on plumbing the (edges, error) pair.
func extractEdges(t *testing.T, e Extractor, r graph.Resource, all []graph.Resource) []graph.Edge {
	t.Helper()
	edges, err := e.Extract(context.Background(), r, sliceLister(all))
	if err != nil {
		t.Fatalf("%T.Extract(%s): unexpected error: %v", e, r.ID(), err)
	}
	return edges
}

// extractAllEdges is extractEdges for a whole Registry.
func extractAllEdges(t *testing.T, reg *Registry, r graph.Resource, all []graph.Resource) []graph.Edge {
	t.Helper()
	edges, err := reg.ExtractAll(context.Background(), r, sliceLister(all))
	if err != nil {
		t.Fatalf("Registry.ExtractAll(%s): unexpected error: %v", r.ID(), err)
	}
	return edges
}
