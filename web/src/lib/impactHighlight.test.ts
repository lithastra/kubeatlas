import cytoscape from 'cytoscape';

import { impactFixture } from '../test/impactFixture';
import { applyImpactHighlight } from './impactHighlight';

test('computed opacity recovers after loading and unrelated edge types stay dimmed', () => {
  const result = impactFixture();
  const cy = cytoscape({ headless: true, styleEnabled: true,
    elements: [
      { data: { id: result.analysis.root.id } },
      ...result.analysis.resources.map((m) => ({ data: { id: m.resource.id } })),
      { data: { id: 'ordinary', source: 'demo/Pod/api', target: result.analysis.root.id, type: 'USES_CONFIGMAP' } },
      { data: { id: 'runtime', source: 'demo/Pod/api', target: result.analysis.root.id, type: 'CALLS_AT_RUNTIME' } },
    ],
    style: [{ selector: 'node[?dimmed]', style: { opacity: 0.2 } }, { selector: 'edge[?dimmed]', style: { opacity: 0.15 } }],
  });
  try {
    applyImpactHighlight(cy);
    // Read the style, as a real rendered frame does, before applying success.
    expect(cy.nodes().first().style('opacity')).toBe('0.2');
    expect(cy.getElementById('ordinary').style('opacity')).toBe('0.15');
    applyImpactHighlight(cy, result);
    expect(cy.nodes().first().style('opacity')).toBe('1');
    expect(cy.getElementById('ordinary').style('opacity')).toBe('1');
    expect(cy.getElementById('runtime').style('opacity')).toBe('0.15');
    applyImpactHighlight(cy);
    expect(cy.nodes().first().style('opacity')).toBe('0.2');
  } finally { cy.destroy(); }
});
