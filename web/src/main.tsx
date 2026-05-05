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

// Provider stack lives here, so App.tsx stays focused on routing +
// layout. Theme is the Headlamp-aligned palette in src/theme.ts.

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
