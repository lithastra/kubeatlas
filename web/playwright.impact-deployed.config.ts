import { defineConfig } from '@playwright/test';

import base from './playwright.config';

// The caller owns the deployed KubeAtlas service and its fixture. No server
// is started here, and a failed capture is never retried by Playwright.
export default defineConfig({
  ...base,
  testMatch: 'impact-deployed.spec.ts',
  testIgnore: [],
  retries: 0,
  outputDir: 'test-results/impact-deployed',
});
