import { defineConfig } from '@playwright/test';

import base from './playwright.config';

// Production browser integration with captured synthetic API responses.
// Independent of the live-cluster smoke suite: no Go server or kubectl needed.
export default defineConfig({
  ...base,
  testMatch: 'impact.spec.ts',
  testIgnore: [],
  retries: 0,
  use: { ...base.use, baseURL: 'http://127.0.0.1:4173' },
  webServer: {
    command: 'npm run preview -- --host 127.0.0.1 --port 4173 --strictPort',
    url: 'http://127.0.0.1:4173',
    reuseExistingServer: false,
  },
});
