import { configureStore, createSlice } from '@reduxjs/toolkit';

// scaffoldSlice exists only so configureStore has at least one reducer
// at construction time. The first real slice (filterSlice) lands in
// P1-T12 alongside the resources page; the selection slice in P1-T17.
// Once a real slice exists, this placeholder can be removed.
const scaffoldSlice = createSlice({
  name: 'scaffold',
  initialState: { ready: true },
  reducers: {},
});

export const store = configureStore({
  reducer: {
    scaffold: scaffoldSlice.reducer,
  },
});

export type RootState = ReturnType<typeof store.getState>;
export type AppDispatch = typeof store.dispatch;
