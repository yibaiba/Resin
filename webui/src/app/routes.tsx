import { lazy, Suspense, type ReactNode } from "react";
import { Navigate, Route, Routes, useLocation, useParams } from "react-router-dom";
import { AppShell } from "../components/AppShell";
import { RequireAuth } from "../features/auth/RequireAuth";
import { useI18n } from "../i18n";
import { RouteErrorBoundary } from "./RouteErrorBoundary";

const LoginPage = lazy(async () => ({ default: (await import("../features/auth/LoginPage")).LoginPage }));
const DashboardPage = lazy(async () => ({ default: (await import("../features/dashboard/DashboardPage")).DashboardPage }));
const EndpointsPage = lazy(async () => ({ default: (await import("../features/endpoints/EndpointsPage")).EndpointsPage }));
const EgressIPsPage = lazy(async () => ({ default: (await import("../features/egress/EgressIPsPage")).EgressIPsPage }));
const GeoIPPage = lazy(async () => ({ default: (await import("../features/geoip/GeoIPPage")).GeoIPPage }));
const NodesPage = lazy(async () => ({ default: (await import("../features/nodes/NodesPage")).NodesPage }));
const PlatformDetailPage = lazy(async () => ({ default: (await import("../features/platforms/PlatformDetailPage")).PlatformDetailPage }));
const PlatformPage = lazy(async () => ({ default: (await import("../features/platforms/PlatformPage")).PlatformPage }));
const RequestLogsPage = lazy(async () => ({ default: (await import("../features/requestLogs/RequestLogsPage")).RequestLogsPage }));
const RulesPage = lazy(async () => ({ default: (await import("../features/rules/RulesPage")).RulesPage }));
const RoutingRulesPage = lazy(async () => ({ default: (await import("../features/routingRules/RoutingRulesPage")).RoutingRulesPage }));
const RoutingDiagnosticsPage = lazy(async () => ({ default: (await import("../features/diagnostics/RoutingDiagnosticsPage")).RoutingDiagnosticsPage }));
const SubscriptionPage = lazy(async () => ({ default: (await import("../features/subscriptions/SubscriptionPage")).SubscriptionPage }));
const SystemConfigPage = lazy(async () => ({ default: (await import("../features/systemConfig/SystemConfigPage")).SystemConfigPage }));

function DeferredRoute({ children }: { children: ReactNode }) {
  const { t } = useI18n();
  return (
    <RouteErrorBoundary>
      <Suspense fallback={<div className="route-page-loading">{t("页面加载中...")}</div>}>
        {children}
      </Suspense>
    </RouteErrorBoundary>
  );
}

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
      <Route path="/login" element={<DeferredRoute><LoginPage /></DeferredRoute>} />

      <Route
        element={
          <RequireAuth>
            <AppShell />
          </RequireAuth>
        }
      >
        <Route path="/" element={<Navigate to="/dashboard" replace />} />
        <Route path="/dashboard" element={<DeferredRoute><DashboardPage /></DeferredRoute>} />
        <Route path="/platforms" element={<DeferredRoute><PlatformPage /></DeferredRoute>} />
        <Route path="/platforms/:platformId" element={<DeferredRoute><PlatformDetailRoute /></DeferredRoute>} />
        <Route path="/subscriptions" element={<DeferredRoute><SubscriptionPage /></DeferredRoute>} />
        <Route path="/nodes" element={<DeferredRoute><NodesRoute /></DeferredRoute>} />
        <Route path="/endpoints" element={<DeferredRoute><EndpointsPage /></DeferredRoute>} />
        <Route path="/rules" element={<DeferredRoute><RulesPage /></DeferredRoute>} />
        <Route path="/routing-rules" element={<DeferredRoute><RoutingRulesPage /></DeferredRoute>} />
        <Route path="/egress-ips" element={<DeferredRoute><EgressIPsPage /></DeferredRoute>} />
        <Route path="/routing-diagnostics" element={<DeferredRoute><RoutingDiagnosticsPage /></DeferredRoute>} />
        <Route path="/request-logs" element={<DeferredRoute><RequestLogsPage /></DeferredRoute>} />
        <Route path="/resources" element={<DeferredRoute><GeoIPPage /></DeferredRoute>} />
        <Route path="/system-config" element={<DeferredRoute><SystemConfigPage /></DeferredRoute>} />
      </Route>

      <Route path="*" element={<Navigate to="/dashboard" replace />} />
    </Routes>
  );
}
