import type { ColumnDef } from "@tanstack/react-table";
import { Check, CircleX } from "lucide-react";
import { Badge } from "../../components/ui/Badge";
import { Card } from "../../components/ui/Card";
import { DataTable } from "../../components/ui/DataTable";
import { useI18n } from "../../i18n";
import { routingLabel } from "../../lib/routing-labels";
import type { DiagnosticCandidate, DiagnosticResponse } from "./types";

type ResultProps = { result: DiagnosticResponse };
type Route = NonNullable<DiagnosticResponse["route"]>;

export function DiagnosticResult({ result }: ResultProps) {
  const route = result.route;
  const platformNames = new Map(route ? [route.root, ...route.fallback].map((platform) => [platform.id, platform.name]) : []);
  return (
    <section className="diagnostic-result" aria-live="polite">
      <DiagnosticSummary result={result} />
      <ProcessDetails result={result} platformNames={platformNames} />
      {route ? <CandidateTable route={route} platformNames={platformNames} /> : null}
      {result.live_tcp?.error ? <div className="callout callout-error diagnostic-errors"><CircleX size={15} /><span>{result.live_tcp.error}</span></div> : null}
    </section>
  );
}

function DiagnosticSummary({ result }: ResultProps) {
  const { t } = useI18n();
  const route = result.route;
  const selected = route?.selection;
  const destination = selected?.egress_ip || t(routingLabel(route?.outcome || result.decision.reason));
  const action = t(routingLabel(result.decision.action));
  return (
    <Card className="platform-list-card platform-directory-card diagnostic-summary-card">
      <div className="list-card-header"><div><h3>{t("诊断摘要")}</h3><p>{result.decision.matched_rule_name || t(routingLabel(result.decision.reason))}</p></div><Badge variant={result.decision.action === "REJECT" ? "danger" : result.decision.action === "DIRECT" ? "info" : "success"}>{action}</Badge></div>
      <dl className="diagnostic-summary-grid">
        <div><dt>{t("路由动作")}</dt><dd>{action}</dd></div>
        <div><dt>{t("最终去向")}</dt><dd>{destination}</dd></div>
        <div><dt>Platform</dt><dd>{selected?.platform_name || result.decision.platform_name || "-"}</dd></div>
        <div><dt>{t("TCP 连通")}</dt><dd className={result.live_tcp?.success ? "text-success" : result.live_tcp ? "text-danger" : undefined}>{result.live_tcp ? formatLiveResult(result.live_tcp, t) : t("未执行")}</dd></div>
      </dl>
    </Card>
  );
}

function ProcessDetails({ result, platformNames }: ResultProps & { platformNames: Map<string, string> }) {
  const { t } = useI18n();
  const route = result.route;
  return (
    <Card className="platform-list-card platform-directory-card diagnostic-process-card">
      <div className="list-card-header"><div><h3>{t("选择过程")}</h3><p>{t("按规则、fallback、候选比较和最终选择顺序展示。")}</p></div></div>
      <div className="diagnostic-process-grid">
        <section className="diagnostic-process-section"><h4>{t("目标规则轨迹")}</h4><RuleTrace result={result} /></section>
        <section className="diagnostic-process-section"><h4>{t("Platform fallback")}</h4><FallbackTrace route={route} platformNames={platformNames} /></section>
        {route ? <section className="diagnostic-process-section"><h4>{t("P2C 节点比较")}</h4><P2CTrace route={route} platformNames={platformNames} /></section> : null}
        {route ? <section className="diagnostic-process-section"><h4>{t("最终选择")}</h4><FinalSelection route={route} /></section> : null}
      </div>
    </Card>
  );
}

function RuleTrace({ result }: ResultProps) {
  const { t } = useI18n();
  const rows = result.decision_trace.rules;
  return <div className="trace-list">{result.decision_trace.environment_bypass_matched ? <TraceRow name={t("环境直连规则")} reason="matched" matched /> : null}{rows.map((rule) => <TraceRow key={rule.id} name={rule.name} reason={rule.reason} matched={rule.matched} />)}{!result.decision_trace.environment_bypass_matched && rows.length === 0 ? <p className="muted">{t("没有需要检查的持久化规则")}</p> : null}</div>;
}

function FallbackTrace({ route, platformNames }: { route?: Route; platformNames: Map<string, string> }) {
  const { t } = useI18n();
  if (!route) return <p className="muted">{t("本次决策不经过 Platform")}</p>;
  const chain = [route.root, ...route.fallback.filter((platform) => platform.id !== route.root.id)];
  return <><ol className="diagnostic-fallback-list">{chain.map((platform, index) => <li key={platform.id}><span>{index + 1}</span>{platform.name}</li>)}</ol>{route.lease ? <dl className="compact-kv"><dt>{t("现有租约")}</dt><dd>{route.lease.egress_ip}</dd><dt>{t("租约 Platform")}</dt><dd>{platformNames.get(route.lease.selected_platform_id) || route.lease.selected_platform_id}</dd><dt>{t("轮换状态")}</dt><dd>{route.lease.rotation_deferred ? t(routingLabel(route.lease.rotation_deferred_reason)) : t("无需延期")}</dd></dl> : null}</>;
}

function P2CTrace({ route, platformNames }: { route: Route; platformNames: Map<string, string> }) {
  const { t } = useI18n();
  if (!route.p2c.length) return <p className="muted">{t("候选不足，无需执行比较。")}</p>;
  return <div className="p2c-list">{route.p2c.map((item) => {
    const firstSelected = item.trace.selected_hash === item.trace.first_node_hash;
    const secondSelected = item.trace.selected_hash === item.trace.second_node_hash;
    return <div className="p2c-row" key={item.platform_id}><strong>{platformNames.get(item.platform_id) || item.platform_id}</strong><span className={firstSelected ? "selected" : ""}>{shortHash(item.trace.first_node_hash)} · {formatScore(item.trace.first_score)}{firstSelected ? ` · ${t("已选择")}` : ""}</span><span className={secondSelected ? "selected" : ""}>{shortHash(item.trace.second_node_hash)} · {formatScore(item.trace.second_score)}{secondSelected ? ` · ${t("已选择")}` : ""}</span></div>;
  })}</div>;
}

function FinalSelection({ route }: { route: Route }) {
  const { t } = useI18n();
  const selection = route.selection;
  if (!selection) return <p className="muted">{t("没有可用节点，请根据上方排除原因修复配置或节点状态。")}</p>;
  return <dl className="compact-kv"><dt>Platform</dt><dd>{selection.platform_name}</dd><dt>{t("出口 IP")}</dt><dd>{selection.egress_ip}</dd><dt>{t("节点")}</dt><dd className="diagnostic-code" title={selection.node_hash}>{shortHash(selection.node_hash)}</dd>{selection.chain?.length ? <><dt>{t("链式前置")}</dt><dd>{selection.chain.map((hop) => hop.platform_name || hop.platform_id).join(" → ")}</dd></> : null}<dt>{t("选择原因")}</dt><dd>{t(routingLabel(selection.reason))}</dd><dt>{t("轮换结果")}</dt><dd>{selection.rotation_status ? t(routingLabel(selection.rotation_status)) : t("未触发轮换")} {selection.rotation_reason ? `· ${t(routingLabel(selection.rotation_reason))}` : ""}</dd></dl>;
}

function CandidateTable({ route, platformNames }: { route: Route; platformNames: Map<string, string> }) {
  const { t } = useI18n();
  const eligible = route.candidates.filter((item) => item.eligible).length;
  const columns: ColumnDef<DiagnosticCandidate>[] = [
    { id: "platform", header: "Platform", cell: ({ row }) => platformNames.get(row.original.platform_id) || row.original.platform_id },
    { accessorKey: "node_hash", header: t("节点"), cell: ({ row }) => <span className="diagnostic-code" title={row.original.node_hash}>{shortHash(row.original.node_hash)}</span> },
    { accessorKey: "egress_ip", header: t("出口 IP"), cell: ({ row }) => <span className="diagnostic-code">{row.original.egress_ip || "-"}</span> },
    { id: "eligible", header: t("结果"), cell: ({ row }) => <Badge variant={row.original.eligible ? "success" : "muted"}>{row.original.eligible ? t("可选") : t("已排除")}</Badge> },
    { id: "capacity", header: t("活跃租约 / 容量"), cell: ({ row }) => `${row.original.active_lease_count} / ${row.original.max_active_leases || "∞"}` },
    { id: "exclusions", header: t("排除原因"), cell: ({ row }) => <span className="diagnostic-errors">{row.original.exclusions.map((reason) => t(routingLabel(reason))).join("，") || "-"}</span> },
  ];
  return <Card className="platform-cards-container subscriptions-table-card diagnostic-candidate-card"><div className="diagnostic-candidate-heading"><h3>{t("候选与排除")}</h3><p>{t("{{eligible}} / {{total}} 个节点可选", { eligible, total: route.candidates.length })}</p></div><DataTable data={route.candidates} columns={columns} getRowId={(item) => `${item.platform_id}-${item.node_hash}`} className="data-table-diagnostic" wrapClassName="diagnostic-table-wrap" /></Card>;
}

function TraceRow({ name, reason, matched }: { name: string; reason: string; matched: boolean }) {
  const { t } = useI18n();
  return <div className={matched ? "trace-row trace-row-matched" : "trace-row"}>{matched ? <Check size={14} /> : <CircleX size={14} />}<strong>{name}</strong><span>{t(routingLabel(reason))}</span></div>;
}

function formatLiveResult(result: NonNullable<DiagnosticResponse["live_tcp"]>, t: (key: string) => string): string {
  if (!result.attempted) return t(routingLabel(result.reason));
  return result.success ? `${result.duration_ms} ms` : t("连接失败");
}

function shortHash(value?: string): string {
  if (!value) return "-";
  return value.length > 12 ? `${value.slice(0, 12)}...` : value;
}

function formatScore(value: number): string {
  return Number.isFinite(value) ? value.toFixed(3) : "-";
}
