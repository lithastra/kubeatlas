import { Component, type ErrorInfo, type ReactNode } from 'react';
import { Alert, AlertTitle, Box, Button, Stack } from '@mui/material';

// ErrorBoundary keeps a thrown render-time error confined to the
// route's main area — the AppShell (top bar + sidebar) stays
// rendered, the user sees what broke, and they can route away. With
// no boundary, React 19 unmounts the whole tree on an uncaught
// throw, producing a fully blank page that's indistinguishable from
// "the bundle never loaded".
//
// Class component because React Hooks still don't have an
// error-boundary equivalent. Reset key wired off route changes by
// the caller (App.tsx).
interface Props {
  children: ReactNode;
  resetKey?: string;
}

interface State {
  error: Error | null;
}

export class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props);
    this.state = { error: null };
  }

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  override componentDidUpdate(prev: Props) {
    // Reset on route change so navigating away from a broken page
    // gives the next page a fair shot.
    if (prev.resetKey !== this.props.resetKey && this.state.error) {
      this.setState({ error: null });
    }
  }

  override componentDidCatch(error: Error, info: ErrorInfo) {
    // Surface to the browser console so DevTools shows the stack
    // alongside the React component trace.
    console.error('ErrorBoundary caught:', error, info.componentStack);
  }

  override render() {
    if (this.state.error) {
      return (
        <Box sx={{ p: 2 }}>
          <Stack spacing={2}>
            <Alert severity="error">
              <AlertTitle>Page failed to render</AlertTitle>
              {this.state.error.message || String(this.state.error)}
            </Alert>
            <Box>
              <Button
                variant="outlined"
                size="small"
                onClick={() => this.setState({ error: null })}
              >
                Try again
              </Button>
            </Box>
            {this.state.error.stack && (
              <Box
                component="pre"
                sx={{
                  p: 1,
                  fontSize: 12,
                  bgcolor: 'background.paper',
                  borderRadius: 1,
                  overflow: 'auto',
                }}
              >
                {this.state.error.stack}
              </Box>
            )}
          </Stack>
        </Box>
      );
    }
    return this.props.children;
  }
}
