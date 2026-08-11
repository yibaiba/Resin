import type { ColumnDef } from "@tanstack/react-table";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Droplets, RefreshCw, Search } from "lucide-react";
import { useDeferredValue, useMemo, useState } from "react";
import { RoutingWorkflowGuide } from "../../components/RoutingWorkflowGuide";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { DataTable } from "../../components/ui/DataTable";
import { Input } from "../../components/ui/Input";
import { OffsetPagination } from "../../components/ui/OffsetPagination";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { routingLabel } from "../../lib/routing-labels";
import { rotationStrategyLabel } from "../platforms/constants";
import type { RotationStrategy } from "../platforms/types";
import { batchDrainEgressIPs, listEgressIPs, rotateEgressLeases, updateEgressPolicy } from "./api";
import { EgressIPDrawer } from "./EgressIPDrawer";
import type { BatchEgressResult, EgressIP, EgressPolicyInput, EgressRotationResult } from "./types";

const EGRESS_PAGE_SIZE_OPTIONS = [25, 50, 100, 200] as const;

export function EgressIPsPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [search, setSearch] = useState("");
  const deferredSearch = useDeferredValue(search);
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(50);
  const [selected, setSelected] = useState<string[]>([]);
  const [editingIP, setEditingIP] = useState<string | null>(null);
  const [resumeAt, setResumeAt] = useState("");
  const [batchResults, setBatchResults] = useState<BatchEgressResult[]>([]);
  const [rotationResults, setRotationResults] = useState<EgressRotationResult[]>([]);
  const egressQuery = useQuery({
    queryKey: ["egress-ips", deferredSearch, page, pageSize],
    queryFn: () => listEgressIPs(deferredSearch, pageSize, page * pageSize),
    placeholderData: (previous) => previous,
    staleTime: 0,
  });
  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["egress-ips"] });
  const policyMutation = useMutation({
    mutationFn: ({ ip, input }: { ip: string; input: EgressPolicyInput }) => updateEgressPolicy(ip, input),
    onSuccess: async () => { await invalidate(); setEditingIP(null); showToast("success", t("出口策略已更新")); },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const drainMutation = useMutation({
    mutationFn: () => batchDrainEgressIPs(selected, resumeAt ? new Date(resumeAt).toISOString() : ""),
    onSuccess: async (response) => { setBatchResults(response.items); await invalidate(); },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const rotateMutation = useMutation({
    mutationFn: ({ ip, strategy }: { ip: string; strategy: RotationStrategy }) => rotateEgressLeases(ip, { strategy }),
    onSuccess: async (response) => { setRotationResults(response.items); await invalidate(); },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const items = egressQuery.data?.items ?? [];
  const totalItems = egressQuery.data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(totalItems / pageSize));
  const searchPending = search !== deferredSearch || egressQuery.isFetching;
  const editing = editingIP ? items.find((item) => item.egress_ip === editingIP) ?? null : null;
  const selectedSet = useMemo(() => new Set(selected), [selected]);

  if (!egressQuery.isPlaceholderData && page >= totalPages) {
    setPage(totalPages - 1);
    if (selected.length > 0) setSelected([]);
    if (editingIP) setEditingIP(null);
  }

  const toggle = (ip: string, checked: boolean) => setSelected((current) => checked ? current.includes(ip) ? current : [...current, ip] : current.filter((item) => item !== ip));
  const changeSearch = (value: string) => {
    setSearch(value);
    setPage(0);
    setSelected([]);
    setEditingIP(null);
  };
  const changePage = (nextPage: number) => {
    setPage(nextPage);
    setSelected([]);
    setEditingIP(null);
  };
  const changePageSize = (nextPageSize: number) => {
    setPageSize(nextPageSize);
    setPage(0);
    setSelected([]);
    setEditingIP(null);
  };
  const drain = () => {
    if (selected.length === 0) return;
    if (window.confirm(t("确认排空选中的 {{count}} 个出口 IP？", { count: selected.length }))) drainMutation.mutate();
  };
  const rotate = (ip: string, strategy: RotationStrategy) => {
    const strategyName = t(rotationStrategyLabel[strategy]);
    if (window.confirm(t("确认按 {{strategy}} 轮换出口 {{ip}} 的关联租约？", { strategy: strategyName, ip }))) rotateMutation.mutate({ ip, strategy });
  };
  const columns = createEgressColumns({
    t,
    items,
    selected: selectedSet,
    disabled: searchPending,
    onToggle: toggle,
    onToggleAll: (checked) => setSelected(checked ? items.map((item) => item.egress_ip) : []),
  });

  return (
    <section className="routing-control-page egress-page">
      <header className="module-header">
        <div><h2>{t("出口 IP 池")}</h2><p className="module-description">{t("控制出口是否接收流量、容量上限，以及现有粘性租约如何迁移。")}</p></div>
        <Button variant="secondary" size="sm" onClick={() => void egressQuery.refetch()} disabled={egressQuery.isFetching}><RefreshCw size={15} className={egressQuery.isFetching ? "spin" : undefined} />{t("刷新")}</Button>
      </header>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      <RoutingWorkflowGuide active="egress" />
      <EgressStateGuide />
      <Card className="platform-list-card platform-directory-card egress-toolbar-card">
        <div className="egress-toolbar">
          <div className="field-group egress-search-field"><label className="field-label" htmlFor="egress-search">{t("查找出口 IP")}</label><label className="search-box" htmlFor="egress-search"><Search size={15} /><Input id="egress-search" value={search} onChange={(event) => changeSearch(event.target.value)} placeholder="203.0.113.10" /></label></div>
          <div className="field-group"><label className="field-label" htmlFor="egress-batch-resume">{t("批量自动恢复时间")}</label><Input id="egress-batch-resume" type="datetime-local" value={resumeAt} onChange={(event) => setResumeAt(event.target.value)} /></div>
          <Button className="egress-batch-action" variant="secondary" onClick={drain} disabled={selected.length === 0 || drainMutation.isPending || searchPending}><Droplets size={15} />{t("排空已选 {{count}} 项", { count: selected.length })}</Button>
        </div>
      </Card>
      {egressQuery.isError ? <div className="callout callout-error"><AlertTriangle size={15} /><span>{formatApiErrorMessage(egressQuery.error, t)}</span></div> : null}
      <Card id="egress-policies" className="platform-cards-container subscriptions-table-card egress-table-card routing-anchor-target" aria-label={t("出口 IP 列表")}>
        {egressQuery.isLoading ? <p className="muted egress-state">{t("正在加载出口 IP...")}</p> : null}
        {!egressQuery.isLoading && items.length === 0 ? <div className="empty-box egress-empty"><Droplets size={18} /><div><strong>{t("暂无出口 IP")}</strong><p>{t("节点完成出口探测并获取公网 IP 后，会自动出现在这里。")}</p></div><Button variant="secondary" size="sm" onClick={() => void egressQuery.refetch()}><RefreshCw size={15} />{t("重新检查")}</Button></div> : null}
        {items.length ? <DataTable data={items} columns={columns} onRowClick={(item) => setEditingIP(item.egress_ip)} selectedRowId={editingIP ?? undefined} getRowId={(item) => item.egress_ip} className="data-table-egress" wrapClassName="egress-table-wrap" /> : null}
        <OffsetPagination
          page={page}
          totalPages={totalPages}
          totalItems={totalItems}
          pageSize={pageSize}
          pageSizeOptions={EGRESS_PAGE_SIZE_OPTIONS}
          disabled={searchPending}
          onPageChange={changePage}
          onPageSizeChange={changePageSize}
        />
      </Card>
      {batchResults.length ? <ResultPanel title={t("批量排空结果")} items={batchResults.map((item) => ({ key: item.egress_ip, ok: item.success, detail: item.error }))} /> : null}
      {rotationResults.length ? <ResultPanel title={t("租约轮换结果")} items={rotationResults.map((item) => ({ key: `${item.platform_id} / ${item.account}`, ok: !item.error, detail: item.error || routingLabel(item.status) }))} /> : null}
      {editing ? <EgressIPDrawer item={editing} pending={policyMutation.isPending || rotateMutation.isPending} onClose={() => setEditingIP(null)} onSave={(input) => policyMutation.mutate({ ip: editing.egress_ip, input })} onRotate={(strategy) => rotate(editing.egress_ip, strategy)} /> : null}
    </section>
  );
}

type EgressColumnOptions = {
  t: ReturnType<typeof useI18n>["t"];
  items: EgressIP[];
  selected: Set<string>;
  disabled: boolean;
  onToggle: (ip: string, checked: boolean) => void;
  onToggleAll: (checked: boolean) => void;
};

function createEgressColumns(options: EgressColumnOptions): ColumnDef<EgressIP>[] {
  const { t } = options;
  const allSelected = options.items.length > 0 && options.items.every((item) => options.selected.has(item.egress_ip));
  const checkbox = (item: EgressIP) => <label className="egress-select" onClick={(event) => event.stopPropagation()}><input type="checkbox" checked={options.selected.has(item.egress_ip)} disabled={options.disabled} onChange={(event) => options.onToggle(item.egress_ip, event.target.checked)} /><span className="sr-only">{t("选择出口 {{ip}}", { ip: item.egress_ip })}</span></label>;
  return [
    { id: "selected", header: () => <label className="egress-select"><input type="checkbox" checked={allSelected} disabled={options.disabled} onChange={(event) => options.onToggleAll(event.target.checked)} /><span className="sr-only">{t("选择全部出口 IP")}</span></label>, cell: ({ row }) => checkbox(row.original) },
    { accessorKey: "egress_ip", header: t("出口 IP"), cell: ({ row }) => <strong className="egress-ip-value">{row.original.egress_ip}</strong> },
    { id: "status", header: t("状态"), cell: ({ row }) => <div className="egress-status-cell"><Badge variant={row.original.status === "ACTIVE" ? "success" : row.original.status === "DRAINING" ? "warning" : "danger"}>{t(routingLabel(row.original.status))}</Badge>{row.original.status !== row.original.configured_status ? <small>{t("配置等待生效：{{status}}", { status: t(routingLabel(row.original.configured_status)) })}</small> : null}</div> },
    { accessorKey: "node_count", header: t("关联节点") },
    { id: "leases", header: t("活跃租约 / 容量"), cell: ({ row }) => `${row.original.active_lease_count} / ${row.original.max_active_leases || t("不限")}` },
    { id: "platforms", header: t("覆盖 Platform"), cell: ({ row }) => row.original.platform_ids.length },
    { id: "resume", header: t("自动恢复"), cell: ({ row }) => row.original.resume_at ? new Date(row.original.resume_at).toLocaleString() : t("未设置") },
  ];
}

function EgressStateGuide() {
  const { t } = useI18n();
  const states = [
    { variant: "success", title: "正常", detail: "接收新租约并继续服务现有租约。" },
    { variant: "warning", title: "排空", detail: "停止新分配，现有租约在下次使用时迁移。" },
    { variant: "danger", title: "禁用", detail: "立即排除该出口，现有租约不再继续使用。" },
  ] as const;
  return <Card className="platform-list-card platform-directory-card egress-state-guide" aria-label={t("出口状态说明")}><div className="list-card-header"><div><h3>{t("出口状态说明")}</h3><p>{t("以下标签仅用于说明状态，不可点击。")}</p></div></div><div className="egress-state-legend">{states.map((state) => <div key={state.title}><Badge variant={state.variant}>{t(state.title)}</Badge><span>{t(state.detail)}</span></div>)}</div></Card>;
}

function ResultPanel({ title, items }: { title: string; items: Array<{ key: string; ok: boolean; detail?: string }> }) {
  const { t } = useI18n();
  return <Card className="operation-results"><h3>{title}</h3>{items.map((item) => <div key={item.key} className={item.ok ? "result-ok" : "result-error"}><strong>{item.key}</strong><span>{item.detail ? t(item.detail) : item.ok ? t("成功") : t("失败")}</span></div>)}</Card>;
}
