import { Accordion, AccordionDetails, AccordionSummary, Alert, Box, Chip, Divider, Stack, Table, TableBody, TableCell, TableHead, TableRow, Typography } from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import { useTranslation } from 'react-i18next';

import type { Edge, Resource } from '../api/types';
import { NeighborView } from './NeighborView';

export interface ResourceDetailPanelProps {
  resource: Resource;
  incoming: Edge[];
  outgoing: Edge[];
  // Server-generated Mermaid flowchart for the one-hop neighbour
  // graph. Empty when the view exceeds MaxResourceNeighbors —
  // we render a "switch to topology" hint instead.
  mermaidText?: string;
}

// ResourceDetailPanel composes the three sections the spec demands:
//   - header: kind / name / namespace + label chips + collapsed annotations
//   - middle: incoming / outgoing edge tables
//   - bottom: NeighborView Mermaid graph
export function ResourceDetailPanel({ resource, incoming, outgoing, mermaidText }: ResourceDetailPanelProps) {
  const { t } = useTranslation('translation');
  const { t: tg } = useTranslation('glossary');

  return (
    <Stack spacing={3}>
      <Box>
        <Typography variant="h4">
          {tg(`kind.${resource.kind}`, { defaultValue: resource.kind })}/{resource.name}
        </Typography>
        <Typography variant="body2" color="text.secondary">
          {resource.namespace || '(cluster-scoped)'}
        </Typography>
        <Stack direction="row" spacing={1} sx={{ mt: 1, flexWrap: 'wrap' }}>
          {Object.entries(resource.labels ?? {}).map(([k, v]) => (
            <Chip key={k} label={`${k}=${v}`} size="small" />
          ))}
        </Stack>
        {resource.annotations && Object.keys(resource.annotations).length > 0 && (
          <Accordion sx={{ mt: 1 }}>
            <AccordionSummary expandIcon={<ExpandMoreIcon />}>
              <Typography variant="body2">Annotations ({Object.keys(resource.annotations).length})</Typography>
            </AccordionSummary>
            <AccordionDetails>
              <Stack spacing={0.5}>
                {Object.entries(resource.annotations).map(([k, v]) => (
                  <Typography key={k} variant="body2" sx={{ fontFamily: 'monospace', fontSize: 12 }}>
                    {k}: {v}
                  </Typography>
                ))}
              </Stack>
            </AccordionDetails>
          </Accordion>
        )}
      </Box>

      <Divider />

      <Stack direction={{ xs: 'column', md: 'row' }} spacing={3}>
        <EdgeTable title={t('list.column.kind') + ' ← Incoming'} edges={incoming} otherEnd="from" />
        <EdgeTable title={t('list.column.kind') + ' → Outgoing'} edges={outgoing} otherEnd="to" />
      </Stack>

      <Divider />

      <Box>
        <Typography variant="h6" sx={{ mb: 1 }}>Neighborhood</Typography>
        {mermaidText ? (
          <NeighborView mermaidText={mermaidText} />
        ) : (
          <Alert severity="info">
            Neighbourhood is too dense to render as a flowchart. Switch to the topology view to explore it.
          </Alert>
        )}
      </Box>
    </Stack>
  );
}

interface EdgeTableProps {
  title: string;
  edges: Edge[];
  otherEnd: 'from' | 'to';
}

function EdgeTable({ title, edges, otherEnd }: EdgeTableProps) {
  const { t: tg } = useTranslation('glossary');
  return (
    <Box sx={{ flex: 1 }}>
      <Typography variant="subtitle2" sx={{ mb: 1 }}>{title}</Typography>
      {edges.length === 0 ? (
        <Typography variant="body2" color="text.secondary">—</Typography>
      ) : (
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>Type</TableCell>
              <TableCell>{otherEnd === 'from' ? 'From' : 'To'}</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {edges.map((e, i) => (
              <TableRow key={`${e.from}-${e.to}-${e.type}-${i}`}>
                <TableCell>{tg(`edge.${e.type}`, { defaultValue: e.type })}</TableCell>
                <TableCell sx={{ fontFamily: 'monospace', fontSize: 12 }}>
                  {otherEnd === 'from' ? e.from : e.to}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </Box>
  );
}
