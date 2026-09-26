# KubeAtlas Documentation Site

Source for [docs.kubeatlas.lithastra.com](https://docs.kubeatlas.lithastra.com),
built with [Docusaurus](https://docusaurus.io/).

## Structure

```
docs/
├── docs/                    # Markdown content (one file per page)
│   ├── intro.md             # Landing page (slug: /)
│   ├── quick-start.md
│   ├── architecture.md
│   └── developer-guide.md
├── src/                     # Custom React/CSS overrides (rare)
├── static/                  # Static assets served at site root
├── docusaurus.config.ts     # Site config (title, URL, navbar, etc.)
├── sidebars.ts              # Sidebar ordering
└── package.json
```

Page order in the sidebar is set explicitly in `sidebars.ts`, not derived
from filesystem order — add new pages there.

## Local development

```bash
cd docs
npm ci                      # install the reviewed lockfile
npm run start               # http://localhost:3000, hot reload
```

The dev server is permissive about broken links so you can iterate
fast. Run `npm run build` (below) before committing to catch real
errors.

### WSL2 note

If `localhost:3000` doesn't open in your Windows browser, bind to all
interfaces:

```bash
npm run start -- --host 0.0.0.0
```

Keep this directory on the WSL ext4 filesystem (`~/...`), **not** on
`/mnt/c/...` — the 9P bridge to NTFS is 5–20× slower for the
file-watcher and `npm install`.

## Production build

```bash
cd docs
npm run build               # outputs to docs/build/
npm run serve               # serves docs/build/ at http://localhost:3000
```

`npm run build` fails on broken internal links and emits warnings
on broken markdown links. A successful local build validates the build output,
not deployment, hosting configuration, or security by itself.

## Toolchain dependency checks

Use Node 20 or later and run these checks before changing the documentation
toolchain or its lockfile:

```bash
npm ci
npm audit --audit-level=low
npm run test:dependencies
npm run typecheck
npm run build
```

Docusaurus remains on 3.10.2. Its current build/development dependencies require
three narrowly scoped overrides, declared in `package.json`:

- `copy-webpack-plugin@11.0.0` and `css-minimizer-webpack-plugin@5.0.1` use
  `serialize-javascript` 7.1.2 or later in the 7.x line. This addresses
  [expression injection](https://github.com/advisories/GHSA-5c6j-r48x-rmvq) and
  [array-like input CPU exhaustion](https://github.com/advisories/GHSA-qj8w-gfj5-8c6v),
  and the 7.1.1 [function-body escaping regression](https://github.com/yahoo/serialize-javascript/security/advisories/GHSA-gfhx-hw2g-v5hg).
  Version 7 requires Node 20, which is already the documentation baseline.
- `sockjs@0.3.24` uses the patched 11.x `uuid` line, starting at 11.1.1, which
  retains the CommonJS `v4()` contract used by SockJS and fixes
  [output-buffer bounds checks](https://github.com/advisories/GHSA-w5hq-g745-h8pq).
  Do not replace it with an ESM-only major without reviewing the caller.

Express is updated within webpack-dev-server's existing 4.x range to resolve a
patched `qs`; it does not need an override. No npm peer checks are bypassed.

The regression tests resolve packages from their actual consumers, exercise CSS
worker serialization and SockJS connection IDs, and bound the security fixtures
without contacting a cluster. These checks and the audit run in documentation CI.
When an upstream parent adopts patched dependencies, remove the corresponding
override and rerun the full checks above. Do not broaden overrides, suppress audit
findings, or downgrade Docusaurus to obtain a green audit.

## Adding or editing a page

1. Create a markdown file under `docs/docs/` with frontmatter:
   ```markdown
   ---
   sidebar_position: 5
   title: My New Page
   ---
   ```
2. Add the page slug to `sidebars.ts` in the order you want it to
   appear.
3. Run `npm run start` to preview.
4. Run `npm run build` to confirm the production build is clean.

## Deployment

This site deploys automatically on push to `main`. The current target
is documented in the project's Phase 0 guide; whichever path is in use
(GitHub Pages or Cloudflare Pages), the build command is `npm run build`
with output directory `build/` and Node 20.

The `url`, `baseUrl`, `organizationName`, and `projectName` in
`docusaurus.config.ts` are the deployment-side knobs — change them
only when migrating between hosts.
