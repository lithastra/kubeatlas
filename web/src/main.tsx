import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { CssBaseline, ThemeProvider, createTheme } from '@mui/material';
import { Provider as ReduxProvider } from 'react-redux';
import { QueryClientProvider } from '@tanstack/react-query';
import { BrowserRouter } from 'react-router-dom';

import { App } from './App';
import { store } from './store';
import { queryClient } from './api/queryClient';

// Provider stack lives here, so App.tsx stays focused on routing +
// layout. Theme is the default MUI light palette for now; the real
// Headlamp-aligned palette lands in P1-T10.
const theme = createTheme({ palette: { mode: 'light' } });

const root = createRoot(document.getElementById('root')!);
root.render(
  <StrictMode>
    <ReduxProvider store={store}>
      <QueryClientProvider client={queryClient}>
        <ThemeProvider theme={theme}>
          <CssBaseline />
          <BrowserRouter>
            <App />
          </BrowserRouter>
        </ThemeProvider>
      </QueryClientProvider>
    </ReduxProvider>
  </StrictMode>
);
