import { defineConfig } from '@playwright/test';
import base from './playwright.config';

// Production UI with local synthetic transport; cached Chromium only.
export default defineConfig({
  ...base, testMatch: 'history.spec.ts', testIgnore: [], retries: 0,
  outputDir: '/tmp/kubeatlas-history-client-browser',
  use: { ...base.use, baseURL: 'http://127.0.0.1:4173', trace: 'off',
    launchOptions: { executablePath: process.env.KUBEATLAS_HISTORY_CHROMIUM },
  },
  webServer: {
    command: 'npm run preview -- --host 127.0.0.1 --port 4173 --strictPort',
    url: 'http://127.0.0.1:4173', reuseExistingServer: false,
  },
});
