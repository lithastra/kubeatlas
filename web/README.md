# Web UI

KubeAtlas Web UI — React 19 + TypeScript + MUI v5 + Redux Toolkit +
TanStack Query + Vite.

The cartography canvas uses Cytoscape.js with the cose-bilkent layout
and five runtime-switchable themes. Key surfaces: full-bleed topology
canvas, command palette, blast-radius mode, time-axis diff, edge-type
filter chips, and the federation cluster strip.

See [architecture.md](../docs/docs/architecture.md) for the full
design and [CONTRIBUTING.md](../CONTRIBUTING.md) for the dev loop.

## Local dependency validation

Run Web checks from this directory:

```bash
npm test -- --runInBand
npm run typecheck
npm run typecheck:e2e
npm run lint
npm run build
```

`npm test` first validates the real Istanbul configuration consumer. The check
resolves `@istanbuljs/load-nyc-config` through `babel-plugin-istanbul`, then checks
its actual YAML parser and that parser's `argparse` dependency against their
installed paths in `package-lock.json`. The parser must also match the explicit
scoped override in `package.json`. These are checks of the active consumer path,
not an unrelated installed copy or proof that every dependency matches the lock.

A lockfile update does not update an existing `node_modules` tree. A version
mismatch fails with the exact dependency/path before configuration parsing.
Reconcile the installed packages with the locked artifacts; do not change package
version labels, remove the override, patch library exports, or drop assertions to
make a stale installation appear valid. Offline recovery requires locally cached
archives whose integrity matches the lockfile and verified package identities.
Validate those artifacts before replacing the affected installed packages, and
preserve the old subtree while diagnosing drift.

The consumer check still exercises inherited YAML settings, typed values,
malformed YAML rejection and rejection of JavaScript-specific tags. Running Jest
directly can help isolate application tests, but does not establish that the
complete `npm test` entry point passed.
