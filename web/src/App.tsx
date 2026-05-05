import { Box, Button, Container, Stack, Typography } from '@mui/material';

// W6 scaffold: the smallest renderable surface that proves the
// React + MUI + provider stack actually compiles and runs. The real
// AppShell with nav drawer + routing lands in P1-T10 (theme) and
// P1-T12 (resources page).
export function App() {
  return (
    <Container maxWidth="md" sx={{ py: 6 }}>
      <Stack spacing={3} alignItems="flex-start">
        <Typography variant="h3" component="h1">
          KubeAtlas
        </Typography>
        <Typography variant="body1" color="text.secondary">
          Kubernetes resource dependency graph. Web UI scaffold — real
          pages land in Phase 1 W7+.
        </Typography>
        <Box>
          <Button variant="contained" color="primary">
            Hello, MUI
          </Button>
        </Box>
      </Stack>
    </Container>
  );
}
