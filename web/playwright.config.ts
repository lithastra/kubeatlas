import { defineConfig, devices } from '@playwright/test';

// E2E config. Tests assume:
//   - The KubeAtlas API server is reachable at http://localhost:8080
//     (the Vite dev server proxies /api and /watch to it).
//   - A populated cluster (PetClinic fixture) is loaded so the
//     resources / topology pages have something to render.
//
// CI brings up the cluster + API + dev server in the e2e workflow.
// Locally: `cd web && npm run dev` against a real cluster, then
// `npx playwright test`.
export default defineConfig({
  testDir: './tests/e2e',
  timeout: 60_000,
  expect: { timeout: 10_000 },
  // E2E exercises a live cluster + WS — parallel runs would race on
  // kubectl scale. Serial keeps the fixture state predictable.
  workers: 1,
  fullyParallel: false,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: process.env.E2E_BASE_URL ?? 'http://localhost:5173',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
