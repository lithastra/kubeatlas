import { mergeOverlayEdges } from './overlay';
import type { Edge, View } from '../api/types';

const view: View = {
  level: 'namespace',
  nodes: [
    { id: 'app/Deployment/frontend', type: 'resource', edge_count_in: 0, edge_count_out: 1 },
    { id: 'app/Deployment/api', type: 'resource', edge_count_in: 1, edge_count_out: 0 },
  ],
  edges: [{ from: 'app/Deployment/frontend', to: 'app/Deployment/api', type: 'ROUTES_TO', count: 1 }],
};

describe('mergeOverlayEdges', () => {
  test('appends a runtime edge between existing nodes', () => {
    const overlay: Edge[] = [
      { from: 'app/Deployment/api', to: 'app/Deployment/frontend', type: 'CALLS_AT_RUNTIME' },
    ];
    const merged = mergeOverlayEdges(view, overlay);
    expect(merged.edges).toHaveLength(2);
    expect(merged.edges[1]).toMatchObject({
      from: 'app/Deployment/api',
      to: 'app/Deployment/frontend',
      type: 'CALLS_AT_RUNTIME',
    });
    // Original view untouched (non-destructive overlay).
    expect(view.edges).toHaveLength(1);
  });

  test('drops an edge whose endpoint is not a node', () => {
    const overlay: Edge[] = [
      { from: 'app/Deployment/frontend', to: 'app/Deployment/ghost', type: 'CALLS_AT_RUNTIME' },
    ];
    const merged = mergeOverlayEdges(view, overlay);
    expect(merged.edges).toHaveLength(1);
  });

  test('skips an edge duplicating an existing (from,to,type)', () => {
    const overlay: Edge[] = [
      { from: 'app/Deployment/frontend', to: 'app/Deployment/api', type: 'ROUTES_TO' },
    ];
    const merged = mergeOverlayEdges(view, overlay);
    expect(merged.edges).toHaveLength(1);
  });

  test('returns the same reference when nothing is added', () => {
    expect(mergeOverlayEdges(view, [])).toBe(view);
    const allDropped: Edge[] = [
      { from: 'x', to: 'y', type: 'CALLS_AT_RUNTIME' },
    ];
    expect(mergeOverlayEdges(view, allDropped)).toBe(view);
  });
});
