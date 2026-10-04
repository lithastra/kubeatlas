import { fileURLToPath } from 'node:url';
import { defineConfig } from '@playwright/test';

import base from './playwright.config';

// The Go test-only fixture serves the actual production assets and API routes.
// Build web/dist and both binaries first; this suite never starts Kubernetes.
export default defineConfig({
  ...base,
  testMatch: 'impact-live.spec.ts',
  testIgnore: [],
  retries: 0,
  outputDir: 'test-results/impact-live',
  reporter: process.env.CI ? [['list'], ['html', { outputFolder: 'playwright-live-report', open: 'never' }]] : 'list',
  use: { ...base.use, baseURL: 'http://127.0.0.1:4174' },
  webServer: {
    command: 'KUBEATLAS_IMPACT_BROWSER_FIXTURE=1 ../../bin/impact-browser-server.test -test.run=^TestImpactBrowserFixtureServer$ -test.timeout=2m',
    cwd: fileURLToPath(new URL('../pkg/api', import.meta.url)),
    url: 'http://127.0.0.1:4174/healthz',
    reuseExistingServer: false,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 15_000 },
  },
});
