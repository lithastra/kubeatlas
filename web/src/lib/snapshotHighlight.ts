import type { Core } from 'cytoscape';
import type { DiffResult } from '../api/types';

// A scoped diff cannot decorate same-named nodes in another member. Empty
// namespaces use actual graph identity (/Kind/name), not the route sentinel _.
export function applySnapshotHighlight(cy: Core, diff: DiffResult | undefined, cluster: string | null): void {
  cy.batch(() => {
    cy.nodes().forEach((node) => {
      node.removeData('added');
      node.removeData('removed');
      node.removeData('modified');
    });
    if (!diff || cluster === null) return;
    for (const [bucket, flag] of [['added', 'added'], ['removed', 'removed'], ['modified', 'modified']] as const) {
      for (const entry of diff[bucket]) {
        const id = `${cluster ? `${cluster}:` : ''}${entry.namespace}/${entry.kind}/${entry.name}`;
        const node = cy.getElementById(id);
        if (node.length && node.data('type') === 'resource') node.data(flag, true);
      }
    }
  });
}
