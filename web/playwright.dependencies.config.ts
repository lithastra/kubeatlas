import { defineConfig } from '@playwright/test';

import base from './playwright.config';

export default defineConfig({
  ...base,
  testMatch: 'dependency-consumers.spec.ts',
  testIgnore: [],
  retries: 0,
  reporter: [['list'], ['html', { outputFolder: 'playwright-dependencies-report', open: 'never' }]],
  outputDir: 'test-results/dependencies',
  use: { ...base.use, baseURL: 'http://127.0.0.1:4174' },
  webServer: {
    command: 'npm run dev -- --host 127.0.0.1 --port 4174 --strictPort',
    url: 'http://127.0.0.1:4174/tests/fixtures/dependency-consumers.html',
    reuseExistingServer: false,
  },
});
