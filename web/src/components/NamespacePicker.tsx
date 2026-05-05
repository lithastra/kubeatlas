import { Autocomplete, TextField } from '@mui/material';
import { useTranslation } from 'react-i18next';

import { useClusterGraph } from '../api/graph';
import { useAppDispatch, useAppSelector } from '../store';
import { setNamespace } from '../store/filterSlice';

// NamespacePicker is the top-of-page namespace selector. The list
// comes from the cluster-level aggregation (each node = one
// namespace). Selection is persisted in Redux so the rest of the page
// (and other pages) can react to it.
export function NamespacePicker() {
  const { t } = useTranslation('translation');
  const dispatch = useAppDispatch();
  const selected = useAppSelector((s) => s.filter.namespace);
  const { data, isLoading } = useClusterGraph();

  const namespaces = (data?.nodes ?? [])
    .map((n) => n.id)
    .filter((id) => id && !id.startsWith('_')) // hide the _cluster bucket
    .sort();

  return (
    <Autocomplete
      sx={{ width: 320 }}
      options={namespaces}
      value={selected}
      loading={isLoading}
      onChange={(_, value) => dispatch(setNamespace(value))}
      renderInput={(params) => (
        <TextField
          {...params}
          label={t('filter.namespace.label')}
          placeholder={t('filter.namespace.all')}
          size="small"
        />
      )}
    />
  );
}
