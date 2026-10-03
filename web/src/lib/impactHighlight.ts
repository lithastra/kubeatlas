import type { Core } from 'cytoscape';

import { impactEdgeKey, impactHighlight } from '../api/impact';
import type { ImpactResponse } from '../api/impactTypes';

export function applyImpactHighlight(cy: Core, response?: ImpactResponse): void {
  const result = impactHighlight(response);
  cy.batch(() => {
    cy.nodes().forEach((node) => {
      // Explicit false invalidates computed style after a loading-state dim.
      // removeData inside a batch can leave the old opacity in Cytoscape.
      node.data('dimmed', !result.nodes.has(String(node.id())));
    });
    cy.edges().forEach((edge) => {
      const key = impactEdgeKey(String(edge.source().id()), String(edge.target().id()), String(edge.data('type')));
      edge.data('dimmed', !result.edges.has(key));
    });
  });
}
