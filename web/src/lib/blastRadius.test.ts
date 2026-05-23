import { computeBlastRadius } from './blastRadius';
import type { View } from '../api/types';

// Tiny fixture: A → B → C, plus B → D and an upstream X → A. Used
// to exercise downstream / upstream / both + the depth cap.
const view: View = {
  level: 'cluster',
  nodes: [
    { id: 'A', type: 'resource', edge_count_in: 1, edge_count_out: 1 },
    { id: 'B', type: 'resource', edge_count_in: 1, edge_count_out: 2 },
    { id: 'C', type: 'resource', edge_count_in: 1, edge_count_out: 0 },
    { id: 'D', type: 'resource', edge_count_in: 1, edge_count_out: 0 },
    { id: 'X', type: 'resource', edge_count_in: 0, edge_count_out: 1 },
  ],
  edges: [
    { from: 'A', to: 'B', count: 1 },
    { from: 'B', to: 'C', count: 1 },
    { from: 'B', to: 'D', count: 1 },
    { from: 'X', to: 'A', count: 1 },
  ],
};

describe('computeBlastRadius', () => {
  test('downstream BFS reaches transitive children', () => {
    const r = computeBlastRadius(view, 'A', 'downstream', Infinity);
    expect([...r.reachable].sort()).toEqual(['A', 'B', 'C', 'D']);
    expect(r.byHop.get(1)).toEqual(['B']);
    expect((r.byHop.get(2) ?? []).sort()).toEqual(['C', 'D']);
  });

  test('depth cap stops the traversal early', () => {
    const r = computeBlastRadius(view, 'A', 'downstream', 1);
    expect([...r.reachable].sort()).toEqual(['A', 'B']);
    expect(r.byHop.get(2)).toBeUndefined();
  });

  test('upstream picks up ancestors only', () => {
    const r = computeBlastRadius(view, 'B', 'upstream', Infinity);
    expect([...r.reachable].sort()).toEqual(['A', 'B', 'X']);
  });

  test('both direction unions upstream and downstream', () => {
    const r = computeBlastRadius(view, 'B', 'both', Infinity);
    expect([...r.reachable].sort()).toEqual(['A', 'B', 'C', 'D', 'X']);
  });
});
