import { createSlice, type PayloadAction } from '@reduxjs/toolkit';

// FilterState is the small bag of UI filter knobs the resources page
// (and later the topology view) reads. Keeping it in Redux rather
// than per-component state means the namespace selection survives a
// tab switch and is queryable from any component (e.g. the topology
// view will respect the same namespace pick).
export interface FilterState {
  // Selected namespace, or null for "no selection" (DataGrid renders
  // empty until the user picks one). v0.1.0 doesn't support an
  // "all-namespaces" view because the cluster-level aggregation
  // already exists for that.
  namespace: string | null;
  // Reserved for P1-T13/T15 — kind filter inside a namespace.
  kind: string | null;
  // Reserved for the search panel.
  search: string;
}

const initialState: FilterState = {
  namespace: null,
  kind: null,
  search: '',
};

export const filterSlice = createSlice({
  name: 'filter',
  initialState,
  reducers: {
    setNamespace(state, action: PayloadAction<string | null>) {
      state.namespace = action.payload;
    },
    setKind(state, action: PayloadAction<string | null>) {
      state.kind = action.payload;
    },
    setSearch(state, action: PayloadAction<string>) {
      state.search = action.payload;
    },
    reset() {
      return initialState;
    },
  },
});

export const { setNamespace, setKind, setSearch, reset: resetFilter } = filterSlice.actions;
