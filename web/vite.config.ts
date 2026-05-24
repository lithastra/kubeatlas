import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Vite config.
//
// Dev mode proxies the four backend surfaces to the Go API server.
// Default target is :8080; override with KUBEATLAS_DEV_API for the
// multicluster fixture (which runs on :18080) or any other port.
// Production builds output to `web/dist/`, which the Go binary
// embeds via `//go:embed all:web/dist`.
const apiTarget = process.env.KUBEATLAS_DEV_API ?? 'http://localhost:8080';

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': apiTarget,
      '/healthz': apiTarget,
      '/readyz': apiTarget,
      '/metrics': apiTarget,
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
