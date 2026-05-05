import { useMemo } from 'react';
import { Alert, Box, CircularProgress, Stack, Typography } from '@mui/material';
import { DataGrid, type GridColDef, type GridRowParams } from '@mui/x-data-grid';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';

import { useNamespaceGraph } from '../api/graph';
import { useAppSelector } from '../store';

// ResourceTable renders the namespace-level aggregation as a sortable
// DataGrid: one row per node (workloads as aggregated, configs / SAs
// / PVCs as raw resources). Row click navigates to the resource
// detail page.
export function ResourceTable() {
  const { t } = useTranslation('translation');
  const navigate = useNavigate();
  const namespace = useAppSelector((s) => s.filter.namespace);
  const { data, isLoading, isError, error } = useNamespaceGraph(namespace);

  const rows = useMemo(() => {
    if (!data) return [];
    return data.nodes.map((n) => ({
      id: n.id,
      kind: n.kind ?? '',
      namespace: n.namespace ?? '',
      name: n.name ?? n.label ?? n.id,
      // Phase 1 v0.1.0 doesn't surface K8s status; leave the column
      // blank so adding it later is just data wiring.
      status: 'N/A',
      // Phase 1 v0.1.0 doesn't compute Age client-side. Real
      // creationTimestamp parsing lands once the resource detail
      // endpoint surfaces it (it already does via Resource.Raw).
      age: '—',
    }));
  }, [data]);

  const columns: GridColDef[] = useMemo(
    () => [
      { field: 'kind', headerName: t('list.column.kind'), width: 160 },
      { field: 'name', headerName: t('list.column.name'), flex: 1, minWidth: 200 },
      { field: 'namespace', headerName: t('list.column.namespace'), width: 160 },
      { field: 'age', headerName: t('list.column.age'), width: 100 },
      { field: 'status', headerName: t('list.column.status'), width: 120 },
    ],
    [t]
  );

  if (!namespace) {
    return (
      <Alert severity="info">{t('filter.namespace.all')}</Alert>
    );
  }
  if (isLoading) {
    return (
      <Stack spacing={1} alignItems="center" sx={{ py: 4 }}>
        <CircularProgress size={24} />
        <Typography variant="body2" color="text.secondary">
          {t('list.loading')}
        </Typography>
      </Stack>
    );
  }
  if (isError) {
    return <Alert severity="error">{(error as Error)?.message ?? 'unknown error'}</Alert>;
  }
  if (rows.length === 0) {
    return <Alert severity="info">{t('list.empty')}</Alert>;
  }
  return (
    <Box sx={{ width: '100%' }}>
      <DataGrid
        autoHeight
        rows={rows}
        columns={columns}
        initialState={{
          pagination: { paginationModel: { pageSize: 25, page: 0 } },
        }}
        pageSizeOptions={[25, 50, 100]}
        onRowClick={(params: GridRowParams) => {
          const row = params.row as { kind: string; namespace: string; name: string };
          const ns = row.namespace || '_';
          navigate(`/resources/${ns}/${row.kind}/${row.name}`);
        }}
        sx={{ cursor: 'pointer' }}
      />
    </Box>
  );
}
