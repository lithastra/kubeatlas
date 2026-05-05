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
    rollupOptions: {
      output: {
        // Split heavy graph + DataGrid bundles out of the main chunk so
        // pages that don't render them (e.g. /resources before P1-T13's
        // topology dependencies) don't pay the parse cost. Order
        // matters — the first matching pattern wins.
        manualChunks: {
          'cytoscape-vendor': ['cytoscape', 'cytoscape-dagre'],
          'mui-grid': ['@mui/x-data-grid'],
          'mermaid-vendor': ['mermaid'],
        },
      },
    },
  },
});
