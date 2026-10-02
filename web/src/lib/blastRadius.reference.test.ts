import fixture from '../../../test/fixtures/impact-analysis/reference-graph.json';
import type { View } from '../api/types';
import { computeBlastRadius } from './blastRadius';

// Shared with Go, using actual child->owner and consumer->resource directions.
// This deliberately characterizes the legacy labels. It is not the v1.7 oracle:
// the new server contract will use dependents/dependencies and explicit scope.
const view: View = {
  level: 'resource',
  nodes: fixture.resources.map((resource) => ({
    id: resource.id,
    type: 'resource',
    edge_count_in: fixture.edges.filter((edge) => edge.to === resource.id).length,
    edge_count_out: fixture.edges.filter((edge) => edge.from === resource.id).length,
  })),
  edges: fixture.edges.map((edge) => ({ from: edge.from, to: edge.to, count: 1 })),
};

describe('legacy blast-radius shared reference graph', () => {
  test.each(fixture.queries)('$name: upstream matches backend incoming traversal', (query) => {
    const result = computeBlastRadius(view, query.root, 'upstream', query.depth);
    expect([...result.reachable].filter((id) => id !== query.root).sort())
      .toEqual([...query.incoming].sort());
  });

  test.each(fixture.queries)('$name: downstream follows dependencies, not dependents', (query) => {
    const result = computeBlastRadius(view, query.root, 'downstream', query.depth);
    expect([...result.reachable].filter((id) => id !== query.root).sort())
      .toEqual([...query.outgoing].sort());
  });

  test('a filtered view can omit a real dependent from the legacy calculation', () => {
    const root = 'demo/ConfigMap/settings';
    const hidden = 'demo/Ingress/api';
    const filtered: View = {
      ...view,
      nodes: view.nodes.filter((node) => node.id !== hidden),
      edges: view.edges.filter((edge) => edge.from !== hidden && edge.to !== hidden),
    };
    expect(computeBlastRadius(view, root, 'upstream', 5).reachable.has(hidden)).toBe(true);
    expect(computeBlastRadius(filtered, root, 'upstream', 5).reachable.has(hidden)).toBe(false);
  });
});
