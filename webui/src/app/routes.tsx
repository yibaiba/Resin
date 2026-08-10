import { Navigate, Route, Routes, useLocation, useParams } from "react-router-dom";
import { AppShell } from "../components/AppShell";
import { LoginPage } from "../features/auth/LoginPage";
import { DashboardPage } from "../features/dashboard/DashboardPage";
import { EndpointsPage } from "../features/endpoints/EndpointsPage";
import { EgressIPsPage } from "../features/egress/EgressIPsPage";
import { GeoIPPage } from "../features/geoip/GeoIPPage";
import { NodesPage } from "../features/nodes/NodesPage";
import { RequireAuth } from "../features/auth/RequireAuth";
import { PlatformDetailPage } from "../features/platforms/PlatformDetailPage";
import { PlatformPage } from "../features/platforms/PlatformPage";
import { RequestLogsPage } from "../features/requestLogs/RequestLogsPage";
import { RulesPage } from "../features/rules/RulesPage";
import { RoutingRulesPage } from "../features/routingRules/RoutingRulesPage";
import { RoutingDiagnosticsPage } from "../features/diagnostics/RoutingDiagnosticsPage";
import { SubscriptionPage } from "../features/subscriptions/SubscriptionPage";
import { SystemConfigPage } from "../features/systemConfig/SystemConfigPage";

function NodesRoute() {
  const location = useLocation();
  return <NodesPage key={location.search} />;
}

function PlatformDetailRoute() {
  const { platformId = "" } = useParams();
  return <PlatformDetailPage key={platformId} />;
}

export function AppRoutes() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />

      <Route
        element={
          <RequireAuth>
            <AppShell />
          </RequireAuth>
        }
      >
        <Route path="/" element={<Navigate to="/dashboard" replace />} />
        <Route path="/dashboard" element={<DashboardPage />} />
        <Route path="/platforms" element={<PlatformPage />} />
        <Route path="/platforms/:platformId" element={<PlatformDetailRoute />} />
        <Route path="/subscriptions" element={<SubscriptionPage />} />
        <Route path="/nodes" element={<NodesRoute />} />
        <Route path="/endpoints" element={<EndpointsPage />} />
        <Route path="/rules" element={<RulesPage />} />
        <Route path="/routing-rules" element={<RoutingRulesPage />} />
        <Route path="/egress-ips" element={<EgressIPsPage />} />
        <Route path="/routing-diagnostics" element={<RoutingDiagnosticsPage />} />
        <Route path="/request-logs" element={<RequestLogsPage />} />
        <Route path="/resources" element={<GeoIPPage />} />
        <Route path="/system-config" element={<SystemConfigPage />} />
      </Route>

      <Route path="*" element={<Navigate to="/dashboard" replace />} />
    </Routes>
  );
}
