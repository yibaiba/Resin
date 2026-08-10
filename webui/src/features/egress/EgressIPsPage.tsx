import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Ban, Droplets, RefreshCw, Search, ShieldCheck } from "lucide-react";
import { useDeferredValue, useState } from "react";
import { RoutingWorkflowGuide } from "../../components/RoutingWorkflowGuide";
import { Button } from "../../components/ui/Button";
import { Input } from "../../components/ui/Input";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { routingLabel } from "../../lib/routing-labels";
import { rotationStrategyLabel } from "../platforms/constants";
import type { RotationStrategy } from "../platforms/types";
import { batchDrainEgressIPs, listEgressIPs, rotateEgressLeases, updateEgressPolicy } from "./api";
import { EgressIPRow } from "./EgressIPRow";
import type { BatchEgressResult, EgressPolicyInput, EgressRotationResult } from "./types";

export function EgressIPsPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [search, setSearch] = useState("");
  const deferredSearch = useDeferredValue(search);
  const [selected, setSelected] = useState<string[]>([]);
  const [resumeAt, setResumeAt] = useState("");
  const [batchResults, setBatchResults] = useState<BatchEgressResult[]>([]);
  const [rotationResults, setRotationResults] = useState<EgressRotationResult[]>([]);
  const egressQuery = useQuery({ queryKey: ["egress-ips", deferredSearch], queryFn: () => listEgressIPs(deferredSearch), placeholderData: (previous) => previous });
  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["egress-ips"] });
  const policyMutation = useMutation({
    mutationFn: ({ ip, input }: { ip: string; input: EgressPolicyInput }) => updateEgressPolicy(ip, input),
    onSuccess: async () => { await invalidate(); showToast("success", t("出口策略已更新")); },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const drainMutation = useMutation({
    mutationFn: () => batchDrainEgressIPs(selected, resumeAt ? new Date(resumeAt).toISOString() : ""),
    onSuccess: async (response) => { setBatchResults(response.items); await invalidate(); },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const rotateMutation = useMutation({
    mutationFn: ({ ip, strategy }: { ip: string; strategy: RotationStrategy }) => rotateEgressLeases(ip, { strategy }),
    onSuccess: (response) => setRotationResults(response.items),
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const toggle = (ip: string, checked: boolean) => setSelected((current) => checked ? [...current, ip] : current.filter((item) => item !== ip));
  const drain = () => {
    if (selected.length === 0) return;
    if (window.confirm(t("确认排空选中的 {{count}} 个出口 IP？", { count: selected.length }))) drainMutation.mutate();
  };
  const rotate = (ip: string, strategy: RotationStrategy) => {
    const strategyName = t(rotationStrategyLabel[strategy]);
    if (window.confirm(t("确认按 {{strategy}} 轮换出口 {{ip}} 的关联租约？", { strategy: strategyName, ip }))) rotateMutation.mutate({ ip, strategy });
  };
  const items = egressQuery.data ?? [];

  return (
    <section className="routing-control-page egress-page">
      <header className="module-header">
        <div><h2>{t("出口 IP 池")}</h2><p className="module-description">{t("控制出口是否接收流量、容量上限，以及现有粘性租约如何迁移。")}</p></div>
        <Button variant="secondary" size="sm" onClick={() => void egressQuery.refetch()} disabled={egressQuery.isFetching}><RefreshCw size={15} className={egressQuery.isFetching ? "spin" : undefined} />{t("刷新")}</Button>
      </header>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      <RoutingWorkflowGuide active="egress" />
      <EgressStateGuide />
      <div className="egress-toolbar">
        <div className="field-group egress-search-field">
          <label className="field-label" htmlFor="egress-search">{t("查找出口 IP")}</label>
          <label className="search-box" htmlFor="egress-search"><Search size={15} /><Input id="egress-search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="203.0.113.10" /></label>
        </div>
        <div className="field-group">
          <label className="field-label" htmlFor="egress-batch-resume">{t("批量自动恢复时间")}</label>
          <Input id="egress-batch-resume" type="datetime-local" value={resumeAt} onChange={(event) => setResumeAt(event.target.value)} />
        </div>
        <Button className="egress-batch-action" variant="secondary" onClick={drain} disabled={selected.length === 0 || drainMutation.isPending}><Droplets size={15} />{t("排空已选 {{count}} 项", { count: selected.length })}</Button>
      </div>
      {egressQuery.isError ? <div className="callout callout-error"><AlertTriangle size={15} /><span>{formatApiErrorMessage(egressQuery.error, t)}</span></div> : null}
      <div className="egress-list">
        {items.map((item) => <EgressIPRow key={item.egress_ip} item={item} checked={selected.includes(item.egress_ip)} pending={policyMutation.isPending || rotateMutation.isPending} onChecked={(checked) => toggle(item.egress_ip, checked)} onSave={(input) => policyMutation.mutate({ ip: item.egress_ip, input })} onRotate={(strategy) => rotate(item.egress_ip, strategy)} />)}
        {egressQuery.isLoading ? <p className="muted egress-state">{t("正在加载出口 IP...")}</p> : null}
        {!egressQuery.isLoading && items.length === 0 ? <div className="empty-box egress-empty"><Droplets size={18} /><div><strong>{t("暂无出口 IP")}</strong><p>{t("节点完成出口探测并获取公网 IP 后，会自动出现在这里。")}</p></div><Button variant="secondary" size="sm" onClick={() => void egressQuery.refetch()}><RefreshCw size={15} />{t("重新检查")}</Button></div> : null}
      </div>
      {batchResults.length ? <ResultPanel title={t("批量排空结果")} items={batchResults.map((item) => ({ key: item.egress_ip, ok: item.success, detail: item.error }))} /> : null}
      {rotationResults.length ? <ResultPanel title={t("租约轮换结果")} items={rotationResults.map((item) => ({ key: `${item.platform_id} / ${item.account}`, ok: !item.error, detail: item.error || routingLabel(item.status) }))} /> : null}
    </section>
  );
}

function EgressStateGuide() {
  const { t } = useI18n();
  const states = [
    { icon: ShieldCheck, title: "正常", detail: "接收新租约并继续服务现有租约。" },
    { icon: Droplets, title: "排空", detail: "停止新分配，现有租约在下次使用时迁移。" },
    { icon: Ban, title: "禁用", detail: "立即排除该出口，现有租约不再继续使用。" },
  ] as const;
  return <section className="egress-state-guide" aria-label={t("出口状态说明")}>{states.map(({ icon: Icon, title, detail }) => <div key={title}><Icon size={17} /><span><strong>{t(title)}</strong><small>{t(detail)}</small></span></div>)}</section>;
}

function ResultPanel({ title, items }: { title: string; items: Array<{ key: string; ok: boolean; detail?: string }> }) {
  const { t } = useI18n();
  return <section className="operation-results"><h3>{title}</h3>{items.map((item) => <div key={item.key} className={item.ok ? "result-ok" : "result-error"}><strong>{item.key}</strong><span>{item.detail ? t(item.detail) : item.ok ? t("成功") : t("失败")}</span></div>)}</section>;
}
