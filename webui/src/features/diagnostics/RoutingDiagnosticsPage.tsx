import { useMutation, useQuery } from "@tanstack/react-query";
import { Activity, AlertTriangle, Play, RefreshCw } from "lucide-react";
import { useState } from "react";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { listPlatforms } from "../platforms/api";
import { diagnoseRoute } from "./api";
import { DiagnosticResult } from "./DiagnosticResult";
import type { DiagnosticRequest } from "./types";

const initialForm: DiagnosticRequest = { mode: "DRY_RUN", platform_name: "", account: "", target: "", required_network: "tcp", timeout: "5s" };

export function RoutingDiagnosticsPage() {
  const { t } = useI18n();
  const { toasts, showToast, dismissToast } = useToast();
  const [form, setForm] = useState(initialForm);
  const [seed, setSeed] = useState("");
  const platformsQuery = useQuery({ queryKey: ["platforms", "diagnostic-choices"], queryFn: () => listPlatforms({ limit: 1000 }), staleTime: 30_000 });
  const mutation = useMutation({
    mutationFn: () => diagnoseRoute({ ...form, seed: seed.trim() ? Number(seed) : undefined }),
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const patch = <Key extends keyof DiagnosticRequest>(key: Key, value: DiagnosticRequest[Key]) => setForm((current) => ({ ...current, [key]: value }));
  const invalidSeed = seed.trim() !== "" && (
    !Number.isSafeInteger(Number(seed)) || Number(seed) < 0
  );

  return (
    <section className="routing-control-page diagnostics-page">
      <header className="module-header"><div><h2>{t("路由诊断")}</h2><p className="module-description">{t("复现路由决策并检查真实 TCP 连通性。")}</p></div>{mutation.data ? <Button variant="secondary" size="sm" onClick={() => mutation.reset()}><RefreshCw size={15} />{t("清除结果")}</Button> : null}</header>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      <Card className="diagnostic-form-panel">
        <div className="diagnostic-mode" role="group" aria-label={t("诊断模式")}><button type="button" className={form.mode === "DRY_RUN" ? "active" : ""} onClick={() => patch("mode", "DRY_RUN")}>DRY RUN</button><button type="button" className={form.mode === "LIVE_TCP" ? "active" : ""} onClick={() => patch("mode", "LIVE_TCP")}>LIVE TCP</button></div>
        <div className="diagnostic-form-grid">
          <div className="field-group diagnostic-target-field"><label className="field-label" htmlFor="diagnostic-target">{t("目标地址")}</label><Input id="diagnostic-target" value={form.target} placeholder="api.example.com:443" onChange={(event) => patch("target", event.target.value)} autoFocus /></div>
          <div className="field-group"><label className="field-label" htmlFor="diagnostic-platform">Platform</label><Select id="diagnostic-platform" value={form.platform_name} onChange={(event) => patch("platform_name", event.target.value)}><option value="">Default</option>{platformsQuery.data?.items.map((item) => <option key={item.id} value={item.name}>{item.name}</option>)}</Select></div>
          <div className="field-group"><label className="field-label" htmlFor="diagnostic-account">Account</label><Input id="diagnostic-account" value={form.account} onChange={(event) => patch("account", event.target.value)} /></div>
          <div className="field-group"><label className="field-label" htmlFor="diagnostic-network">{t("网络")}</label><Select id="diagnostic-network" value={form.required_network} onChange={(event) => patch("required_network", event.target.value as "tcp" | "udp")}><option value="tcp">TCP</option><option value="udp">UDP</option></Select></div>
          <div className="field-group"><label className="field-label" htmlFor="diagnostic-seed">Seed</label><Input id="diagnostic-seed" inputMode="numeric" invalid={invalidSeed} value={seed} onChange={(event) => setSeed(event.target.value)} /></div>
          <div className="field-group"><label className="field-label" htmlFor="diagnostic-timeout">{t("超时")}</label><Input id="diagnostic-timeout" value={form.timeout} disabled={form.mode === "DRY_RUN"} onChange={(event) => patch("timeout", event.target.value)} /></div>
        </div>
        {form.mode === "LIVE_TCP" && form.required_network === "udp" ? <div className="callout callout-warning"><AlertTriangle size={15} /><span>{t("LIVE TCP 仅支持 TCP 网络")}</span></div> : null}
        <Button onClick={() => mutation.mutate()} disabled={!form.target.trim() || invalidSeed || mutation.isPending || (form.mode === "LIVE_TCP" && form.required_network === "udp")}><Play size={15} />{mutation.isPending ? t("诊断中...") : t("运行诊断")}</Button>
      </Card>
      {mutation.isPending ? <div className="diagnostic-running"><Activity size={18} className="spin" /><span>{t("正在计算路由...")}</span></div> : null}
      {mutation.data ? <DiagnosticResult result={mutation.data} /> : null}
    </section>
  );
}
