import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { Provider as ReduxProvider } from 'react-redux';
import { QueryClientProvider } from '@tanstack/react-query';
import { BrowserRouter } from 'react-router-dom';
import { I18nextProvider } from 'react-i18next';

// Cartography theme: bundled fonts (offline-first — no CDN), CSS
// custom-property layer, and the React provider that holds the
// active theme name and rebuilds the MUI theme on change.
import '@fontsource/ibm-plex-sans/400.css';
import '@fontsource/ibm-plex-sans/500.css';
import '@fontsource/ibm-plex-sans/600.css';
import '@fontsource/ibm-plex-mono/400.css';
import '@fontsource/inria-serif/400.css';
import '@fontsource/inria-serif/700.css';
import { AtlasThemeProvider } from './theme';

import { App } from './App';
import { store } from './store';
import { queryClient } from './api/queryClient';
import i18n from './i18n';
import { WatchClient } from './api/websocket';

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
          <AtlasThemeProvider>
            <BrowserRouter>
              <App />
            </BrowserRouter>
          </AtlasThemeProvider>
        </I18nextProvider>
      </QueryClientProvider>
    </ReduxProvider>
  </StrictMode>
);
