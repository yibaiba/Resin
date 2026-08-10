import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Droplets, RefreshCw, Search } from "lucide-react";
import { useState } from "react";
import { Button } from "../../components/ui/Button";
import { Input } from "../../components/ui/Input";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import type { RotationStrategy } from "../platforms/types";
import { batchDrainEgressIPs, listEgressIPs, rotateEgressLeases, updateEgressPolicy } from "./api";
import { EgressIPRow } from "./EgressIPRow";
import type { BatchEgressResult, EgressPolicyInput, EgressRotationResult } from "./types";

export function EgressIPsPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [resumeAt, setResumeAt] = useState("");
  const [batchResults, setBatchResults] = useState<BatchEgressResult[]>([]);
  const [rotationResults, setRotationResults] = useState<EgressRotationResult[]>([]);
  const egressQuery = useQuery({ queryKey: ["egress-ips", search], queryFn: () => listEgressIPs(search), placeholderData: (previous) => previous });
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
    if (window.confirm(t("确认按 {{strategy}} 轮换出口 {{ip}} 的关联租约？", { strategy, ip }))) {
      rotateMutation.mutate({ ip, strategy });
    }
  };
  const items = egressQuery.data ?? [];

  return (
    <section className="routing-control-page egress-page">
      <header className="module-header"><div><h2>{t("出口 IP 池")}</h2><p className="module-description">{t("统一控制出口生命周期、粘性容量与轮换。")}</p></div><Button variant="secondary" size="sm" onClick={() => void egressQuery.refetch()} disabled={egressQuery.isFetching}><RefreshCw size={15} className={egressQuery.isFetching ? "spin" : undefined} />{t("刷新")}</Button></header>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      <div className="egress-toolbar">
        <label className="search-box" htmlFor="egress-search"><Search size={15} /><Input id="egress-search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder={t("搜索出口 IP")} /></label>
        <Input type="datetime-local" value={resumeAt} onChange={(event) => setResumeAt(event.target.value)} aria-label={t("批量恢复时间")} />
        <Button variant="secondary" onClick={drain} disabled={selected.length === 0 || drainMutation.isPending}><Droplets size={15} />{t("批量排空")} ({selected.length})</Button>
      </div>
      {egressQuery.isError ? <div className="callout callout-error"><AlertTriangle size={15} /><span>{formatApiErrorMessage(egressQuery.error, t)}</span></div> : null}
      <div className="egress-table-wrap">
        <table className="egress-table"><thead><tr><th></th><th>{t("出口 IP")}</th><th>{t("状态")}</th><th>{t("活跃租约")}</th><th>{t("配置状态")}</th><th>{t("最大活跃租约")}</th><th>{t("恢复时间")}</th><th>{t("操作")}</th></tr></thead><tbody>{items.map((item) => <EgressIPRow key={item.egress_ip} item={item} checked={selected.includes(item.egress_ip)} pending={policyMutation.isPending || rotateMutation.isPending} onChecked={(checked) => toggle(item.egress_ip, checked)} onSave={(input) => policyMutation.mutate({ ip: item.egress_ip, input })} onRotate={(strategy) => rotate(item.egress_ip, strategy)} />)}</tbody></table>
        {egressQuery.isLoading ? <p className="muted egress-state">{t("正在加载出口 IP...")}</p> : null}
        {!egressQuery.isLoading && items.length === 0 ? <div className="empty-box"><Droplets size={18} /><p>{t("暂无出口 IP")}</p></div> : null}
      </div>
      {batchResults.length ? <ResultPanel title={t("批量排空结果")} items={batchResults.map((item) => ({ key: item.egress_ip, ok: item.success, detail: item.error }))} /> : null}
      {rotationResults.length ? <ResultPanel title={t("租约轮换结果")} items={rotationResults.map((item) => ({ key: `${item.platform_id} / ${item.account}`, ok: !item.error, detail: item.error || item.status }))} /> : null}
    </section>
  );
}

function ResultPanel({ title, items }: { title: string; items: Array<{ key: string; ok: boolean; detail?: string }> }) {
  return <section className="operation-results"><h3>{title}</h3>{items.map((item) => <div key={item.key} className={item.ok ? "result-ok" : "result-error"}><strong>{item.key}</strong><span>{item.detail || (item.ok ? "OK" : "ERROR")}</span></div>)}</section>;
}
