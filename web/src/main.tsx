import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { CssBaseline, ThemeProvider } from '@mui/material';
import { Provider as ReduxProvider } from 'react-redux';
import { QueryClientProvider } from '@tanstack/react-query';
import { BrowserRouter } from 'react-router-dom';
import { I18nextProvider } from 'react-i18next';

import { App } from './App';
import { store } from './store';
import { queryClient } from './api/queryClient';
import { theme } from './theme';
import i18n from './i18n';
import { WatchClient } from './api/websocket';

// Provider stack lives here, so App.tsx stays focused on routing +
// layout. Theme is the Headlamp-aligned palette in src/theme.ts.

// Boot the WebSocket once at module load so reconnection backoff
// survives client-side route changes. Server-side events invalidate
// React Query caches so any active hook refetches.
const watchClient = new WatchClient({ queryClient });
watchClient.start();
// Keep a hard reference so the client isn't GC'd in dev hot-reload
// scenarios that re-evaluate the module.
;(window as unknown as { __kubeatlasWatch?: WatchClient }).__kubeatlasWatch = watchClient;

const root = createRoot(document.getElementById('root')!);
root.render(
  <StrictMode>
    <ReduxProvider store={store}>
      <QueryClientProvider client={queryClient}>
        <I18nextProvider i18n={i18n}>
          <ThemeProvider theme={theme}>
            <CssBaseline />
            <BrowserRouter>
              <App />
            </BrowserRouter>
          </ThemeProvider>
        </I18nextProvider>
      </QueryClientProvider>
    </ReduxProvider>
  </StrictMode>
);
