import cytoscape from 'cytoscape';
import { applySnapshotHighlight } from './snapshotHighlight';
import type { DiffResult } from '../api/types';

test('member and cluster-scoped identities are exact; blocked/loading reads clear prior flags', () => {
  const cy = cytoscape({ headless: true, elements: ['east:/Node/shared', 'west:/Node/shared', '/Node/shared', '_/Node/shared', 'east:demo/Pod/aggregate'].map((id) => ({ data: { id, type: id.endsWith('aggregate') ? 'aggregated' : 'resource' } })) });
  const result: DiffResult = { from: '', to: '', added: [{ namespace: '', kind: 'Node', name: 'shared', ts: '', eventType: 'add' }, { namespace: 'demo', kind: 'Pod', name: 'aggregate', ts: '', eventType: 'add' }], modified: [], removed: [] };
  try {
    applySnapshotHighlight(cy, result, 'east');
    expect(cy.getElementById('east:/Node/shared').data('added')).toBe(true);
    for (const id of ['west:/Node/shared', '/Node/shared', '_/Node/shared', 'east:demo/Pod/aggregate']) expect(cy.getElementById(id).data('added')).toBeUndefined();
    applySnapshotHighlight(cy, undefined, 'west');
    expect(cy.nodes().some((node) => !!node.data('added'))).toBe(false);
    applySnapshotHighlight(cy, result, '');
    expect(cy.getElementById('/Node/shared').data('added')).toBe(true);
    applySnapshotHighlight(cy, result, null);
    expect(cy.nodes().some((node) => !!node.data('added'))).toBe(false);
  } finally { cy.destroy(); }
});
