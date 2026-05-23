import { Box, Stack, Typography } from '@mui/material';
import { useTranslation } from 'react-i18next';

import { NamespacePicker } from '../components/NamespacePicker';
import { ResourceTable } from '../components/ResourceTable';
import { Icon } from '../design';
import { useSearchOverlay } from '../shell';

// ResourcesPage: namespace dropdown on top, table below.
// Selecting a namespace fires the namespace-level aggregation;
// clicking a row routes into the resource detail view.
//
// Outer Box owns the page padding so the content doesn't butt up
// against the left cluster strip. TopologyPage handles its own
// layout (full-bleed absolute positioning) so the shell doesn't
// impose padding globally.
export function ResourcesPage() {
  const { t } = useTranslation('translation');
  const search = useSearchOverlay();
  return (
    <Box sx={{ padding: 'var(--atlas-space-8)', width: '100%', overflow: 'auto' }}>
      <Stack spacing={2}>
        {/* Title + search button on the same row. Search summons the
            ⌘K palette so the keyboard shortcut and the mouse path
            land on the same widget. */}
        <Stack direction="row" alignItems="center" spacing={2}>
          <Typography variant="h4" sx={{ flexGrow: 1 }}>
            {t('page.resources.title')}
          </Typography>
          <Box
            component="button"
            type="button"
            onClick={() => search.setOpen(true)}
            aria-label="Search resources (⌘K)"
            sx={{
              display: 'flex',
              alignItems: 'center',
              gap: 1,
              padding: '8px 14px',
              border: '1px solid var(--atlas-border)',
              backgroundColor: 'var(--atlas-surface)',
              color: 'var(--atlas-text-2)',
              cursor: 'pointer',
              fontFamily: 'var(--atlas-font-ui)',
              fontSize: 13,
              minWidth: 240,
              '&:hover': {
                borderColor: 'var(--atlas-select)',
                color: 'var(--atlas-text-1)',
              },
              '&:focus-visible': {
                outline: '2px solid var(--atlas-select)',
                outlineOffset: 1,
              },
            }}
          >
            <Icon name="search" size={14} />
            <Box component="span" sx={{ flexGrow: 1, textAlign: 'left' }}>
              Search resources…
            </Box>
            <Box
              component="span"
              sx={{
                fontFamily: 'var(--atlas-font-mono)',
                fontSize: 11,
                color: 'var(--atlas-text-3)',
                border: '1px solid var(--atlas-border)',
                padding: '1px 6px',
                borderRadius: 2,
              }}
            >
              ⌘K
            </Box>
          </Box>
        </Stack>
        <NamespacePicker />
        <ResourceTable />
      </Stack>
    </Box>
  );
}
