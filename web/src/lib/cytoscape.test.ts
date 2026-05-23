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
    // nodeLabel reconstructs "Kind/Name" when the view doesn't
    // supply an explicit label, so passthrough kinds (HPA,
    // ConfigMap, ServiceAccount) read the same way as workloads.
    expect(els[0].data.label).toBe('Deployment/api');
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

import {
  buildAtlasStylesheet,
  classifyKind,
  edgeStyleFor,
  isInferredKind,
  mix,
  paletteFor,
} from './cytoscape';

describe('classifyKind (canonical + CRD fallback)', () => {
  it('maps canonical Kinds to their family', () => {
    expect(classifyKind('Deployment')).toBe('workload');
    expect(classifyKind('ConfigMap')).toBe('configuration');
    expect(classifyKind('ServiceAccount')).toBe('identity');
    expect(classifyKind('Service')).toBe('network');
    expect(classifyKind('PersistentVolumeClaim')).toBe('storage');
  });

  it('falls back on Binding suffix to identity', () => {
    expect(classifyKind('AzureIdentityBinding')).toBe('identity');
  });

  it('falls back on Policy in the name to network', () => {
    expect(classifyKind('PodSecurityPolicy')).toBe('network');
  });

  it('falls back on Route/Gateway suffix to network', () => {
    expect(classifyKind('VirtualService')).toBe('network');
    expect(classifyKind('TCPRoute')).toBe('network');
  });

  it('falls back to custom for truly unknown', () => {
    expect(classifyKind('Workflow')).toBe('workload'); // ends in Set/Workload/Cluster/Job rule
    expect(classifyKind('Frobnicator')).toBe('custom');
  });
});

describe('isInferredKind', () => {
  it('returns true for non-canonical Kinds', () => {
    expect(isInferredKind('Workflow')).toBe(true);
    expect(isInferredKind('Deployment')).toBe(false);
  });
});

describe('edgeStyleFor', () => {
  it('returns the canonical channel choices for OWNS', () => {
    const s = edgeStyleFor('OWNS');
    expect(s.weight).toBe('heavy');
    expect(s.dash).toBe('solid');
    expect(s.domain).toBe('structural');
    expect(s.arrow).toBe('filled-tri');
  });

  it('flags ROUTES_TO as flow-animated', () => {
    expect(edgeStyleFor('ROUTES_TO').flow).toBe(true);
  });

  it('falls back to a sensible default for unknown edges', () => {
    const s = edgeStyleFor('UNKNOWN_EDGE_TYPE');
    expect(s.weight).toBe('medium');
    expect(s.domain).toBe('structural');
  });
});

describe('mix', () => {
  it('returns a at t=1 and b at t=0 (bg-dominant scale)', () => {
    expect(mix('#ff0000', '#00ff00', 1).toLowerCase()).toBe('#ff0000');
    expect(mix('#ff0000', '#00ff00', 0).toLowerCase()).toBe('#00ff00');
  });

  it('clamps near the midpoint', () => {
    const m = mix('#ff0000', '#00ff00', 0.5);
    expect(m.toLowerCase()).toBe('#808000');
  });
});

describe('buildAtlasStylesheet', () => {
  it('emits per-edge-type rules for every canonical edge type', () => {
    const rules = buildAtlasStylesheet(paletteFor('parchment')) as Array<{
      selector: string;
    }>;
    const edgeSelectors = rules.map((r) => r.selector).filter((s) => s.startsWith('edge[type ='));
    expect(edgeSelectors).toContain('edge[type = "OWNS"]');
    expect(edgeSelectors).toContain('edge[type = "USES_CONFIGMAP"]');
    expect(edgeSelectors).toContain('edge[type = "BINDS_PLATFORM_IDENTITY"]');
  });

  it('emits a rule per node family', () => {
    const rules = buildAtlasStylesheet(paletteFor('slate')) as Array<{ selector: string }>;
    for (const f of ['workload', 'configuration', 'identity', 'network', 'storage', 'custom']) {
      expect(rules.map((r) => r.selector)).toContain(`node[family = "${f}"]`);
    }
  });
});
