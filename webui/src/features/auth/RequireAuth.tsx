import { AlertTriangle, RefreshCw } from "lucide-react";
import { useEffect, useState, type ReactElement } from "react";
import { Navigate, useLocation } from "react-router-dom";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { useI18n } from "../../i18n";
import { apiRequest, ApiError } from "../../lib/api-client";
import { queryClient } from "../../lib/query-client";
import { useAuthStore } from "./auth-store";

type RequireAuthProps = {
  children: ReactElement;
};

type ValidationStatus = "checking" | "allowed" | "denied" | "error";

type ValidationState = {
  token: string | null;
  status: ValidationStatus;
  error: string;
};

function validationErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    return `${error.status}: ${error.message}`;
  }
  return error instanceof Error ? error.message : String(error);
}

export function RequireAuth({ children }: RequireAuthProps) {
  const { t } = useI18n();
  const token = useAuthStore((state) => state.token);
  const clearToken = useAuthStore((state) => state.clearToken);
  const location = useLocation();
  const [retryVersion, setRetryVersion] = useState(0);
  const [validation, setValidation] = useState<ValidationState>({
    token: null,
    status: "checking",
    error: "",
  });

  useEffect(() => {
    let active = true;
    const controller = new AbortController();

    const checkAuthMode = async () => {
      try {
        await apiRequest("/api/v1/system/info", {
          auth: Boolean(token),
          token: token || undefined,
          signal: controller.signal,
        });
        if (!active) {
          return;
        }
        setValidation({ token, status: "allowed", error: "" });
      } catch (error) {
        if (!active) {
          return;
        }
        if (error instanceof ApiError && error.status === 401 && token) {
          queryClient.clear();
          clearToken();
          return;
        }
        if (error instanceof ApiError && error.status === 401) {
          setValidation({ token, status: "denied", error: "" });
          return;
        }
        setValidation({ token, status: "error", error: validationErrorMessage(error) });
      }
    };

    void checkAuthMode();

    return () => {
      active = false;
      controller.abort();
    };
  }, [clearToken, retryVersion, token]);

  if (validation.token !== token || validation.status === "checking") {
    return <div className="route-page-loading">{t("正在验证管理员会话...")}</div>;
  }
  if (validation.status === "allowed") {
    return children;
  }
  if (validation.status === "error") {
    const retry = () => {
      setValidation({ token, status: "checking", error: "" });
      setRetryVersion((version) => version + 1);
    };
    return (
      <main className="login-layout">
        <Card className="login-card auth-validation-error" role="alert">
          <div className="login-header">
            <div className="brand-logo" aria-hidden="true"><AlertTriangle size={18} /></div>
            <h1 className="login-title">{t("无法验证管理员会话")}</h1>
          </div>
          <p className="muted">{t("Resin API 暂时不可用，当前令牌未被清除。")}</p>
          <p className="field-error">{validation.error}</p>
          <Button className="w-full" onClick={retry}>
            <RefreshCw size={16} />
            {t("重试连接")}
          </Button>
        </Card>
      </main>
    );
  }

  const next = `${location.pathname}${location.search}`;
  return <Navigate to={`/login?next=${encodeURIComponent(next)}`} replace />;
}
