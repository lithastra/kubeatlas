/* ============================================================
 * design/index.ts — public surface of the cartography primitive
 * layer. Views and shell consume primitives from here, never from
 * MUI directly (except for niche cases the design hasn't covered).
 *
 * CommandPalette and ContextMenu remain intentionally absent —
 * they wire into specific view behaviours (search, node
 * right-click) and will land alongside the views that drive them
 * so each can be designed against its mockup in one pass.
 * ============================================================ */
export { Icon, type AtlasIconName } from './Icon';
export { Panel, type AtlasPanelVariant, type AtlasPanelPadding } from './Panel';
export { StatusPill, type AtlasStatusVariant, type AtlasStatusSize } from './StatusPill';
export { AtlasChip, type AtlasChipVariant } from './AtlasChip';
export { AtlasTooltip, type AtlasTooltipVariant } from './AtlasTooltip';
export { RadialMenu, type RadialMenuOption } from './RadialMenu';
