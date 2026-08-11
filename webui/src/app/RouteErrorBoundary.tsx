import { AlertTriangle, RefreshCw } from "lucide-react";
import { Component, type ReactNode } from "react";
import { useLocation } from "react-router-dom";
import { Button } from "../components/ui/Button";
import { Card } from "../components/ui/Card";
import { useI18n } from "../i18n";

type BoundaryProps = {
  children: ReactNode;
  renderError: (error: Error) => ReactNode;
};

type BoundaryState = {
  error: Error | null;
};

class RouteRenderBoundary extends Component<BoundaryProps, BoundaryState> {
  state: BoundaryState = { error: null };

  static getDerivedStateFromError(error: unknown): BoundaryState {
    return { error: error instanceof Error ? error : new Error(String(error)) };
  }

  componentDidCatch(error: Error): void {
    console.error("Route rendering failed", error);
  }

  render(): ReactNode {
    if (this.state.error) {
      return this.props.renderError(this.state.error);
    }
    return this.props.children;
  }
}

export function RouteErrorBoundary({ children }: { children: ReactNode }) {
  const { t } = useI18n();
  const location = useLocation();
  const renderError = (error: Error) => (
    <Card className="route-load-error" role="alert">
      <div className="route-load-error-title">
        <AlertTriangle size={18} />
        <h2>{t("页面加载失败")}</h2>
      </div>
      <p>{t("页面资源加载失败，请重新加载应用。")}</p>
      <code>{error.message}</code>
      <Button size="sm" onClick={() => window.location.reload()}>
        <RefreshCw size={15} />
        {t("重新加载")}
      </Button>
    </Card>
  );

  return <RouteRenderBoundary key={location.pathname} renderError={renderError}>{children}</RouteRenderBoundary>;
}
