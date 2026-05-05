import { Navigate, Route, Routes } from 'react-router-dom';

import { AppShell } from './components/AppShell';
import { DocsPage } from './pages/DocsPage';
import { ResourcePage } from './pages/ResourcePage';
import { ResourcesPage } from './pages/ResourcesPage';
import { SearchPage } from './pages/SearchPage';
import { TopologyPage } from './pages/TopologyPage';

// App is the routing root. AppShell renders the persistent chrome
// (top bar + nav drawer); each route renders inside its main area.
//
// Adding a page means: create the component under src/pages/, add a
// <Route> here, and add a nav entry in AppShell's navItems.
export function App() {
  return (
    <AppShell>
      <Routes>
        <Route path="/" element={<Navigate to="/resources" replace />} />
        <Route path="/resources" element={<ResourcesPage />} />
        <Route path="/resources/:namespace/:kind/:name" element={<ResourcePage />} />
        <Route path="/topology" element={<TopologyPage />} />
        <Route path="/search" element={<SearchPage />} />
        <Route path="/docs" element={<DocsPage />} />
      </Routes>
    </AppShell>
  );
}
