import { useMutation, useQuery } from "@tanstack/react-query";
import { Activity, AlertTriangle, FlaskConical, Play, RadioTower, RefreshCw, SlidersHorizontal } from "lucide-react";
import { useState } from "react";
import { RoutingWorkflowGuide } from "../../components/RoutingWorkflowGuide";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { cn } from "../../lib/cn";
import { listPlatforms } from "../platforms/api";
import { diagnoseRoute } from "./api";
import { DiagnosticResult } from "./DiagnosticResult";
import type { DiagnosticMode, DiagnosticRequest } from "./types";

const initialForm: DiagnosticRequest = { mode: "DRY_RUN", platform_name: "", account: "", target: "", required_network: "tcp", timeout: "5s" };

export function RoutingDiagnosticsPage() {
  const { t } = useI18n();
  const { toasts, showToast, dismissToast } = useToast();
  const [form, setForm] = useState(initialForm);
  const [seed, setSeed] = useState("");
  const [showAdvanced, setShowAdvanced] = useState(false);
  const platformsQuery = useQuery({ queryKey: ["platforms", "diagnostic-choices"], queryFn: () => listPlatforms({ limit: 1000 }), staleTime: 30_000 });
  const mutation = useMutation({
    mutationFn: () => diagnoseRoute({ ...form, seed: seed.trim() ? Number(seed) : undefined }),
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const patch = <Key extends keyof DiagnosticRequest>(key: Key, value: DiagnosticRequest[Key]) => setForm((current) => ({ ...current, [key]: value }));
  const invalidSeed = seed.trim() !== "" && (!Number.isSafeInteger(Number(seed)) || Number(seed) < 0);
  const liveUDP = form.mode === "LIVE_TCP" && form.required_network === "udp";

  return (
    <section className="routing-control-page diagnostics-page">
      <header className="module-header">
        <div><h2>{t("路由诊断")}</h2><p className="module-description">{t("输入一次真实请求的关键条件，逐步解释规则、fallback、候选节点和最终出口。")}</p></div>
        {mutation.data ? <Button variant="secondary" size="sm" onClick={() => mutation.reset()}><RefreshCw size={15} />{t("清除结果")}</Button> : null}
      </header>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      <RoutingWorkflowGuide active="diagnostics" />
      <Card id="diagnostic-form" className="platform-list-card platform-directory-card diagnostic-form-panel routing-anchor-target">
        <form className="diagnostic-form-content" onSubmit={(event) => { event.preventDefault(); mutation.mutate(); }}>
          <DiagnosticModePicker mode={form.mode} onChange={(mode) => patch("mode", mode)} />
          <div className="diagnostic-form-grid">
            <div className="field-group diagnostic-target-field">
              <label className="field-label" htmlFor="diagnostic-target">{t("目标地址")}</label>
              <Input id="diagnostic-target" value={form.target} placeholder="https://api.example.com/path" onChange={(event) => patch("target", event.target.value)} autoFocus />
              <span className="field-help">{t("填写完整 URL 或 host:port，便于准确识别主机和端口。")}</span>
            </div>
            <div className="field-group">
              <label className="field-label" htmlFor="diagnostic-platform">{t("起始 Platform")}</label>
              <Select id="diagnostic-platform" value={form.platform_name} onChange={(event) => patch("platform_name", event.target.value)}><option value="">{t("未指定（规则选择，默认 Default）")}</option>{platformsQuery.data?.items.map((item) => <option key={item.id} value={item.name}>{item.name}</option>)}</Select>
              <span className="field-help">{t("用于复现客户端显式指定 Platform 的情况。")}</span>
            </div>
            <div className="field-group">
              <label className="field-label" htmlFor="diagnostic-account">Account</label>
              <Input id="diagnostic-account" value={form.account} placeholder={t("可选，例如 customer-42")} onChange={(event) => patch("account", event.target.value)} />
              <span className="field-help">{t("留空不会创建或复用粘性租约。")}</span>
            </div>
            <div className="field-group">
              <label className="field-label" htmlFor="diagnostic-network">{t("请求网络")}</label>
              <Select id="diagnostic-network" value={form.required_network} onChange={(event) => patch("required_network", event.target.value as "tcp" | "udp")}><option value="tcp">TCP</option><option value="udp">UDP</option></Select>
              <span className="field-help">{t("影响目标规则和节点能力过滤。")}</span>
            </div>
          </div>
          <div className="diagnostic-advanced">
            <Button className="diagnostic-advanced-toggle" variant="ghost" aria-expanded={showAdvanced} aria-controls="diagnostic-advanced-fields" onClick={() => setShowAdvanced((current) => !current)}><SlidersHorizontal size={16} /><span><strong>{t("高级复现参数")}</strong><small>{t("需要稳定复现抽样或调整连接超时时再设置")}</small></span></Button>
            {showAdvanced ? <div id="diagnostic-advanced-fields" className="diagnostic-advanced-grid">
              <div className="field-group"><label className="field-label" htmlFor="diagnostic-seed">Seed</label><Input id="diagnostic-seed" inputMode="numeric" invalid={invalidSeed} aria-invalid={invalidSeed} aria-describedby={invalidSeed ? "diagnostic-seed-error" : undefined} value={seed} onChange={(event) => setSeed(event.target.value)} />{invalidSeed ? <span id="diagnostic-seed-error" className="field-error" role="alert">{t("{{field}} 必须是非负整数", { field: "Seed" })}</span> : null}<span className="field-help">{t("相同输入与 Seed 会复现相同的候选抽样。")}</span></div>
              <div className="field-group"><label className="field-label" htmlFor="diagnostic-timeout">{t("连接超时")}</label><Input id="diagnostic-timeout" value={form.timeout} disabled={form.mode === "DRY_RUN"} onChange={(event) => patch("timeout", event.target.value)} /><span className="field-help">{form.mode === "DRY_RUN" ? t("只看路由时不会建立连接。") : t("例如 5s；仅限制本次 TCP 连接测试。")}</span></div>
            </div> : null}
          </div>
          {liveUDP ? <div className="callout callout-warning"><AlertTriangle size={15} /><span>{t("TCP 连通验证不支持 UDP；请切换为只看路由或选择 TCP。")}</span></div> : null}
          <div className="diagnostic-submit-row"><span>{form.mode === "DRY_RUN" ? t("本次不会连接目标，也不会修改租约和节点健康。") : t("本次会按诊断选中的路径建立一次 TCP 连接。")}</span><Button type="submit" disabled={!form.target.trim() || invalidSeed || mutation.isPending || liveUDP}><Play size={15} />{mutation.isPending ? t("诊断中...") : t("运行诊断")}</Button></div>
        </form>
      </Card>
      {mutation.isPending ? <div className="diagnostic-running"><Activity size={18} className="spin" /><span>{t("正在计算路由...")}</span></div> : null}
      {mutation.data ? <DiagnosticResult result={mutation.data} /> : null}
    </section>
  );
}

function DiagnosticModePicker({ mode, onChange }: { mode: DiagnosticMode; onChange: (mode: DiagnosticMode) => void }) {
  const { t } = useI18n();
  const modes = [
    { value: "DRY_RUN", icon: FlaskConical, title: "只看路由结果", detail: "解释选择过程，不发起网络连接。" },
    { value: "LIVE_TCP", icon: RadioTower, title: "验证 TCP 连通", detail: "按选定路径建立一次真实连接。" },
  ] as const;
  return <div className="diagnostic-mode platform-detail-tabs" role="group" aria-label={t("诊断模式")}>{modes.map(({ value, icon: Icon, title, detail }) => <button key={value} type="button" aria-pressed={mode === value} className={cn("platform-detail-tab", mode === value && "platform-detail-tab-active")} onClick={() => onChange(value)}><Icon size={16} /><span><strong>{t(title)}</strong><small>{t(detail)}</small></span></button>)}</div>;
}
