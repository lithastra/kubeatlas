import { Navigate, Route, Routes, useLocation } from 'react-router-dom';

import { AtlasShell } from './shell';
import { ErrorBoundary } from './components/ErrorBoundary';
import { PolicyPage } from './pages/PolicyPage';
import { ResourcePage } from './pages/ResourcePage';
import { ResourcesPage } from './pages/ResourcesPage';
import { SnapshotsPage } from './pages/SnapshotsPage';
import { TopologyPage } from './pages/TopologyPage';

// App is the routing root. AtlasShell renders the persistent chrome
// (top bar + nav drawer); each route renders inside its main area.
//
// Routes are wrapped in an ErrorBoundary so a render-time throw in a
// page (e.g. cytoscape failing to register dagre) surfaces inline
// instead of blanking the whole React tree. The boundary resets on
// route change.
//
// Adding a page means: create the component under src/pages/, add a
// <Route> here, and add a nav entry in AtlasShell's navItems.
export function App() {
  const location = useLocation();
  return (
    <AtlasShell>
      <ErrorBoundary resetKey={location.pathname}>
        <Routes>
          <Route path="/" element={<Navigate to="/resources" replace />} />
          <Route path="/resources" element={<ResourcesPage />} />
          <Route path="/resources/:namespace/:kind/:name" element={<ResourcePage />} />
          <Route path="/topology" element={<TopologyPage />} />
          <Route path="/policy" element={<PolicyPage />} />
          <Route path="/snapshots" element={<SnapshotsPage />} />
        </Routes>
      </ErrorBoundary>
    </AtlasShell>
  );
}
