# Cartography theme system

This directory implements the v1.3 cartography design system — five
runtime-switchable themes (Parchment, Survey, Terrain, Ink, Slate)
sharing one token contract.

## Files

| File | What it is | Hand-edit? |
|---|---|---|
| `atlas-themes.css` | CSS custom properties: shared `:root` scales plus five `[data-theme]` blocks. | No — derived. |
| `themePalettes.ts` | Typed palette data for MUI + the future Cytoscape stylesheet. | No — derived. |
| `theme.ts` | MUI v5 theme builder, one per atlas theme. | Yes (system shape). |
| `themeController.ts` | Runtime `data-theme` switching + first-load preference + persistence. | Yes (controller logic). |
| `index.ts` | Clean re-exports. | Yes. |

## Source of truth

The two derived files (`atlas-themes.css`, `themePalettes.ts`) come
from the design tree maintained outside this repo at
[`lithastra/kubeatlas-design`](https://github.com/lithastra/kubeatlas-design).
Inputs there:

- `tokens.json` — theme-independent scales (typography, spacing,
  motion, edge geometry, chrome dimensions, grid pattern).
- `starter/themes.tokens.json` — the five colour palettes.

The generator (`starter/generate.mjs` in that repo) emits the two
derived files. To update KubeAtlas: re-run the generator there and
copy its output here. The shape stays stable; only values change.

The visual rationale (why these specific colours, weights, motion
curves) lives in the design tree's `01-design-system.md`,
`02-edge-encoding.md`, `03-node-system.md`, and `06-themes.md`.

## Switching themes at runtime

```ts
import { setTheme } from './theme';
setTheme('slate');                          // user explicit
import { initTheme } from './theme';
const initial = initTheme();                // call once at startup
```

`setTheme` writes `data-theme` on `<html>` (re-skins every CSS-var
consumer) and persists the choice to `localStorage`. The MUI side
re-builds via `getAtlasTheme(name)`, which the React root passes to
`<ThemeProvider theme=…>` — see `main.tsx`.
