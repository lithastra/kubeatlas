import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Vite config.
//
// Dev mode proxies the four backend surfaces to the Go API server on
// :8080 so the same-origin assumptions in `src/api/client.ts` hold
// without CORS gymnastics. Production builds output to `web/dist/`,
// which the Go binary embeds via `//go:embed all:web/dist` (see
// cmd/kubeatlas/embed.go added in P1-T24).
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8080',
      '/healthz': 'http://localhost:8080',
      '/readyz': 'http://localhost:8080',
      '/metrics': 'http://localhost:8080',
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
  },
});
