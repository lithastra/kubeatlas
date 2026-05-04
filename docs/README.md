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
npm install                 # first run only
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
on broken markdown links — same behaviour as the deploy build, so
"build clean locally" means "deploys clean".

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
