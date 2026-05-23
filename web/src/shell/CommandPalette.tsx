/* ============================================================
 * CommandPalette — ⌘K search overlay.
 *
 * Centered, modal, ~480×500 surface that floats over the graph (not
 * a route change). The operator types a query, the server's /search
 * endpoint returns ranked Resource matches, the palette lists them,
 * and the operator picks one with arrow keys + Enter (or click) to
 * open it in the right detail panel.
 *
 * The query also feeds back into the underlying graph via the
 * SearchContext — TopologyView reads matchedIds and decorates the
 * canvas so matches stay legible after the overlay closes. ESC
 * closes the overlay; matches stay highlighted until cleared.
 * ============================================================ */
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from 'react';
import { Box, Stack, Typography } from '@mui/material';

import { useSearch } from '../api/graph';
import type { Resource } from '../api/types';
import { Icon, Panel } from '../design';
import { NodeDetailPanel } from '../components/NodeDetailPanel';
import { useRightPanel } from './RightPanelContext';
import { useSearchOverlay } from './SearchContext';

const DEBOUNCE_MS = 120;
const MAX_RESULTS = 20;

export function CommandPalette() {
  const { open, setOpen, setMatchedIds, clearMatches } = useSearchOverlay();
  const { setContent } = useRightPanel();
  const [query, setQuery] = useState('');
  const [debounced, setDebounced] = useState('');
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const listRef = useRef<HTMLDivElement | null>(null);

  // Debounce the input so each keystroke doesn't fire a request.
  useEffect(() => {
    const id = window.setTimeout(() => setDebounced(query.trim()), DEBOUNCE_MS);
    return () => window.clearTimeout(id);
  }, [query]);

  const { data, isFetching, isError } = useSearch({ q: debounced, limit: MAX_RESULTS });
  const matches = useMemo(() => data?.matches ?? [], [data]);

  // Push matched IDs into shared state so the canvas highlights
  // matches even when the operator dismisses the overlay.
  useEffect(() => {
    if (!debounced) {
      clearMatches();
      return;
    }
    setMatchedIds(matches.map(resourceId));
  }, [debounced, matches, setMatchedIds, clearMatches]);

  // Reset input + selection each time the overlay opens. Focus the
  // input on the same tick so ⌘K → type is instant.
  useEffect(() => {
    if (open) {
      setQuery('');
      setDebounced('');
      setActive(0);
      // Defer to next tick so the input has actually mounted.
      const id = window.setTimeout(() => inputRef.current?.focus(), 0);
      return () => window.clearTimeout(id);
    }
    return undefined;
  }, [open]);

  // Clamp the active index when the result set shrinks.
  useEffect(() => {
    if (active >= matches.length) setActive(Math.max(0, matches.length - 1));
  }, [active, matches.length]);

  const close = useCallback(() => setOpen(false), [setOpen]);

  const openMatch = useCallback(
    (r: Resource) => {
      setContent(<NodeDetailPanel nodeId={resourceId(r)} />);
      setOpen(false);
    },
    [setContent, setOpen],
  );

  const onKeyDown = useCallback(
    (e: ReactKeyboardEvent<HTMLDivElement>) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        close();
        return;
      }
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        setActive((i) => (matches.length === 0 ? 0 : (i + 1) % matches.length));
        return;
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault();
        setActive((i) =>
          matches.length === 0 ? 0 : (i - 1 + matches.length) % matches.length,
        );
        return;
      }
      if (e.key === 'Enter') {
        e.preventDefault();
        const hit = matches[active];
        if (hit) openMatch(hit);
      }
    },
    [active, close, matches, openMatch],
  );

  // Scroll the active row into view as the operator presses arrow keys.
  useEffect(() => {
    if (!listRef.current) return;
    const row = listRef.current.querySelector<HTMLElement>(
      `[data-palette-row="${active}"]`,
    );
    row?.scrollIntoView({ block: 'nearest' });
  }, [active]);

  if (!open) return null;

  const resultsHint = data
    ? `${data.total} result${data.total === 1 ? '' : 's'}${
        data.truncated ? ` · showing ${matches.length}` : ''
      }`
    : debounced
      ? 'searching…'
      : 'type to search';

  return (
    <Box
      role="dialog"
      aria-modal="true"
      aria-label="Command palette"
      onKeyDown={onKeyDown}
      sx={{
        position: 'fixed',
        inset: 0,
        zIndex: 1200,
        display: 'flex',
        alignItems: 'flex-start',
        justifyContent: 'center',
        paddingTop: '10vh',
        // Subtle backdrop dim — graph stays readable, palette dominates.
        backgroundColor:
          'color-mix(in srgb, var(--atlas-text-1) 20%, transparent)',
      }}
      onClick={(e) => {
        // Click on the backdrop (not the panel) closes the overlay.
        if (e.target === e.currentTarget) close();
      }}
    >
      <Panel
        variant="panel"
        padding={0}
        ariaLabel="Search palette"
        sx={{
          width: 480,
          maxWidth: '90vw',
          maxHeight: '80vh',
          display: 'flex',
          flexDirection: 'column',
          overflow: 'hidden',
        }}
      >
        <Box
          sx={{
            display: 'flex',
            alignItems: 'center',
            gap: 1,
            padding: 'var(--atlas-space-3) var(--atlas-space-4)',
            backgroundColor: 'var(--atlas-bg-elevated, var(--atlas-bg))',
            borderBottom: '1px solid var(--atlas-border)',
          }}
        >
          <Icon name="search" size={16} />
          <Box
            component="input"
            ref={inputRef}
            value={query}
            onChange={(e) => setQuery(e.currentTarget.value)}
            placeholder="Search resources… (name, kind:Pod, ns:foo)"
            aria-label="Search query"
            sx={{
              flexGrow: 1,
              border: 'none',
              outline: 'none',
              background: 'transparent',
              fontFamily: 'var(--atlas-font-ui)',
              fontSize: 15,
              color: 'var(--atlas-text-1)',
              '::placeholder': { color: 'var(--atlas-text-3)' },
            }}
          />
          <Typography
            component="span"
            sx={{
              fontFamily: 'var(--atlas-font-mono)',
              fontSize: 11,
              color: 'var(--atlas-text-3)',
            }}
          >
            ⌘K
          </Typography>
        </Box>

        <Box
          sx={{
            padding: 'var(--atlas-space-2) var(--atlas-space-4)',
            fontFamily: 'var(--atlas-font-mono)',
            fontSize: 10,
            letterSpacing: '0.04em',
            color: 'var(--atlas-text-3)',
            textTransform: 'uppercase',
            borderBottom: '1px solid var(--atlas-border)',
          }}
        >
          {resultsHint}
          {isError && ' · search failed'}
          {data?.warning && ' · linear scan (Tier 1)'}
        </Box>

        <Box ref={listRef} sx={{ overflowY: 'auto', flexGrow: 1 }}>
          {matches.length === 0 && debounced && !isFetching && (
            <Box
              sx={{
                padding: 'var(--atlas-space-4)',
                fontFamily: 'var(--atlas-font-ui)',
                fontSize: 13,
                color: 'var(--atlas-text-3)',
              }}
            >
              No matches.
            </Box>
          )}
          {matches.map((r, i) => (
            <ResultRow
              key={resourceId(r)}
              resource={r}
              query={debounced}
              active={i === active}
              index={i}
              onHover={() => setActive(i)}
              onClick={() => openMatch(r)}
            />
          ))}
        </Box>

        <Box
          sx={{
            display: 'flex',
            gap: 3,
            alignItems: 'center',
            padding: 'var(--atlas-space-2) var(--atlas-space-4)',
            backgroundColor: 'var(--atlas-text-1)',
            color: 'var(--atlas-bg)',
            fontFamily: 'var(--atlas-font-mono)',
            fontSize: 11,
          }}
        >
          <HintKey k="↑↓" label="navigate" />
          <HintKey k="↩" label="open" />
          <HintKey k="Esc" label="close (matches stay)" />
        </Box>
      </Panel>
    </Box>
  );
}

function HintKey({ k, label }: { k: string; label: string }) {
  return (
    <Stack direction="row" spacing={0.75} alignItems="center">
      <Box component="span" sx={{ color: 'var(--atlas-bg)' }}>
        {k}
      </Box>
      <Box
        component="span"
        sx={{
          fontFamily: 'var(--atlas-font-ui)',
          color: 'color-mix(in srgb, var(--atlas-bg) 70%, transparent)',
        }}
      >
        {label}
      </Box>
    </Stack>
  );
}

interface ResultRowProps {
  resource: Resource;
  query: string;
  active: boolean;
  index: number;
  onHover: () => void;
  onClick: () => void;
}

function ResultRow({ resource, query, active, index, onHover, onClick }: ResultRowProps) {
  return (
    <Box
      data-palette-row={index}
      role="option"
      aria-selected={active}
      onMouseEnter={onHover}
      onClick={onClick}
      sx={{
        position: 'relative',
        padding: 'var(--atlas-space-3) var(--atlas-space-4)',
        cursor: 'pointer',
        borderLeft: active ? '3px solid var(--atlas-select)' : '3px solid transparent',
        backgroundColor: active
          ? 'color-mix(in srgb, var(--atlas-select) 8%, transparent)'
          : 'transparent',
      }}
    >
      <Typography
        component="div"
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 13,
          color: 'var(--atlas-text-1)',
        }}
      >
        <Box component="span" sx={{ color: 'var(--atlas-text-3)' }}>
          {resource.namespace ? `${resource.namespace}/` : ''}
        </Box>
        {highlight(resource.name, query)}
      </Typography>
      <Typography
        component="div"
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 11,
          color: 'var(--atlas-text-3)',
          mt: 0.25,
        }}
      >
        {resource.kind}
        {resource.groupVersion ? ` · ${resource.groupVersion}` : ''}
      </Typography>
    </Box>
  );
}

// Wrap the first case-insensitive occurrence of `query` in `text` in a
// styled <span>. Falls back to the raw text when the query is empty
// or doesn't match (most ranked results match on a field other than
// .name — wrapping nothing is fine).
function highlight(text: string, query: string) {
  if (!query) return text;
  const i = text.toLowerCase().indexOf(query.toLowerCase());
  if (i < 0) return text;
  return (
    <>
      {text.slice(0, i)}
      <Box
        component="span"
        sx={{ color: 'var(--atlas-select)', fontWeight: 600 }}
      >
        {text.slice(i, i + query.length)}
      </Box>
      {text.slice(i + query.length)}
    </>
  );
}

// resourceId derives the canonical [namespace]/[kind]/[name] id the
// graph uses for nodes. Mirrors graph.Resource.ID() on the Go side
// (cluster-prefixed IDs land when federation is on; the palette
// stays single-cluster for v1.3 — the multi-cluster picker carries
// that wiring in M5.6).
function resourceId(r: Resource): string {
  const ns = r.namespace || '_';
  return `${ns}/${r.kind}/${r.name}`;
}
