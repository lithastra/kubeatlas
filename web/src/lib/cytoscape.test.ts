import { elementsFromView } from './cytoscape';
import type { View } from '../api/types';

describe('elementsFromView', () => {
  it('emits a node element per view node, carrying kind and label data', () => {
    const v: View = {
      level: 'namespace',
      nodes: [
        {
          id: 'demo/Deployment/api',
          type: 'aggregated',
          kind: 'Deployment',
          namespace: 'demo',
          name: 'api',
          edge_count_in: 0,
          edge_count_out: 0,
        },
      ],
      edges: [],
    };
    const els = elementsFromView(v);
    expect(els).toHaveLength(1);
    expect(els[0].group).toBe('nodes');
    expect(els[0].data.id).toBe('demo/Deployment/api');
    expect(els[0].data.kind).toBe('Deployment');
    expect(els[0].data.label).toBe('api');
    expect(els[0].data.type).toBe('aggregated');
  });

  it('emits an edge element per view edge with source / target / typeLabel', () => {
    const v: View = {
      level: 'namespace',
      nodes: [],
      edges: [{ from: 'a', to: 'b', type: 'OWNS', count: 1 }],
    };
    const els = elementsFromView(v);
    expect(els).toHaveLength(1);
    expect(els[0].group).toBe('edges');
    expect(els[0].data.source).toBe('a');
    expect(els[0].data.target).toBe('b');
    expect(els[0].data.typeLabel).toBe('OWNS');
  });

  it('falls back to label / id for nodes without a name', () => {
    const v: View = {
      level: 'cluster',
      nodes: [
        { id: 'kube-system', type: 'aggregated', label: 'kube-system', edge_count_in: 0, edge_count_out: 0 },
        { id: '_cluster', type: 'aggregated', edge_count_in: 0, edge_count_out: 0 },
      ],
      edges: [],
    };
    const els = elementsFromView(v);
    expect(els[0].data.label).toBe('kube-system');
    expect(els[1].data.label).toBe('_cluster');
  });
});
