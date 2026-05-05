import { execFileSync } from 'node:child_process';
import { expect, test } from '@playwright/test';

// Smoke tests for the v0.1.0 happy path. They assume a populated
// cluster: the e2e workflow seeds it with the PetClinic fixture
// (>=5 resources in the petclinic namespace, several Deployments
// suitable for kubectl scale).
//
// Selectors are chosen for stability:
//   - DataGrid: role="row" + data-id (set by MUI on data rows)
//   - Topology canvas: data-testid="topology-canvas"
//   - Resource header: data-testid="resource-detail-header"
// Avoid asserting on i18n text — labels can move under translation.

const NAMESPACE = process.env.E2E_NAMESPACE ?? 'petclinic';

function kubectl(...args: string[]) {
  // execFileSync does not spawn a shell, so even if NAMESPACE were
  // attacker-controlled there is no injection surface — args go
  // straight into argv.
  execFileSync('kubectl', args, { stdio: 'ignore' });
}

test.describe('KubeAtlas smoke', () => {
  test('Resources page renders the DataGrid with rows after picking a namespace', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 4 })).toBeVisible();

    const ns = page.getByRole('combobox');
    await ns.click();
    await page.getByRole('option', { name: NAMESPACE }).click();

    const grid = page.getByRole('grid');
    await expect(grid).toBeVisible();
    // DataGrid renders one row per resource node. PetClinic baseline
    // is ~16 resource kinds; assert >=5 to leave headroom.
    const rows = grid.locator('[role="row"][data-id]');
    await expect.poll(async () => rows.count(), { timeout: 15_000 }).toBeGreaterThanOrEqual(5);
  });

  test('Topology cluster view renders the cytoscape canvas', async ({ page }) => {
    await page.goto('/topology');
    const canvas = page.getByTestId('topology-canvas');
    await expect(canvas).toBeVisible();
    // Cytoscape mounts three stacked HTMLCanvasElements (node, drag,
    // selection layers) inside its container.
    await expect(canvas.locator('canvas')).toHaveCount(3, { timeout: 15_000 });
  });

  test('Resource detail page opens when a row is clicked', async ({ page }) => {
    await page.goto('/');
    const ns = page.getByRole('combobox');
    await ns.click();
    await page.getByRole('option', { name: NAMESPACE }).click();

    const grid = page.getByRole('grid');
    const firstRow = grid.locator('[role="row"][data-id]').first();
    await expect(firstRow).toBeVisible({ timeout: 15_000 });
    await firstRow.click();

    await expect(page).toHaveURL(/\/resources\//);
    await expect(page.getByTestId('resource-detail-header')).toBeVisible();
  });

  test('WebSocket reflects a kubectl scale within a few seconds', async ({ page }) => {
    test.skip(!process.env.E2E_KUBECTL, 'requires kubectl on PATH (set E2E_KUBECTL=1 in the e2e workflow)');

    await page.goto('/');
    const ns = page.getByRole('combobox');
    await ns.click();
    await page.getByRole('option', { name: NAMESPACE }).click();

    const grid = page.getByRole('grid');
    await expect(grid).toBeVisible();

    // Bump the customers Deployment by one replica. The informer
    // should fire a GraphUpdate; the resource table re-fetches and
    // the DataGrid re-renders. We can't easily diff replica counts
    // from the table (no replicas column in v0.1.0), so we assert
    // the grid stays populated and the page hasn't errored — the
    // real WS-vs-HTTP correctness test lives in
    // pkg/api/websocket_test.go.
    kubectl('-n', NAMESPACE, 'scale', 'deploy/customers', '--replicas=2');
    await page.waitForTimeout(2000);
    await expect(grid).toBeVisible();
    await expect(page.getByText('failed', { exact: false })).toHaveCount(0);
    kubectl('-n', NAMESPACE, 'scale', 'deploy/customers', '--replicas=1');
  });
});
