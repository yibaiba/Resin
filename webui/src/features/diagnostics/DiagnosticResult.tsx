import { ArrowRight, Check, CircleX, GitBranch, Network, RadioTower } from "lucide-react";
import { Badge } from "../../components/ui/Badge";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { routingLabel } from "../../lib/routing-labels";
import type { DiagnosticResponse } from "./types";

type ResultProps = { result: DiagnosticResponse };
type Route = NonNullable<DiagnosticResponse["route"]>;

export function DiagnosticResult({ result }: ResultProps) {
  const route = result.route;
  const platformNames = new Map(route?.fallback.map((platform) => [platform.id, platform.name]));
  return (
    <section className="diagnostic-result" aria-live="polite">
      <DiagnosticOutcome result={result} />
      <div className="diagnostic-grid">
        <RuleTraceSection result={result} />
        <FallbackSection route={route} platformNames={platformNames} />
      </div>
      {route ? <>
        <CandidateSection route={route} platformNames={platformNames} />
        <div className="diagnostic-grid">
          <P2CSection route={route} platformNames={platformNames} />
          <FinalSelectionSection route={route} />
        </div>
      </> : null}
      {result.live_tcp?.error ? <div className="callout callout-error diagnostic-errors"><CircleX size={15} /><span>{result.live_tcp.error}</span></div> : null}
    </section>
  );
}

function DiagnosticOutcome({ result }: ResultProps) {
  const { t } = useI18n();
  const route = result.route;
  const selected = route?.selection?.egress_ip || routingLabel(route?.outcome || result.decision.reason);
  return <div className={cn("card", "diagnostic-outcome", result.live_tcp && "has-live-tcp")}>
    <OutcomeStep icon={GitBranch} label={t("路由动作")} value={t(routingLabel(result.decision.action))} />
    <ArrowRight size={16} aria-hidden="true" />
    <OutcomeStep icon={Network} label={t("最终去向")} value={t(selected)} />
    {result.live_tcp ? <><ArrowRight size={16} aria-hidden="true" /><OutcomeStep icon={RadioTower} label={t("TCP 连通")} value={formatLiveResult(result.live_tcp, t)} tone={result.live_tcp.success ? "success" : "danger"} /></> : null}
  </div>;
}

function OutcomeStep({ icon: Icon, label, value, tone }: { icon: typeof GitBranch; label: string; value: string; tone?: "success" | "danger" }) {
  return <div><span className="diagnostic-step-icon"><Icon size={17} /></span><div><small>{label}</small><strong className={tone ? `text-${tone}` : undefined}>{value}</strong></div></div>;
}

function RuleTraceSection({ result }: ResultProps) {
  const { t } = useI18n();
  const decision = result.decision;
  return <section className="card diagnostic-section">
    <div className="section-heading"><div><h3>{t("目标规则轨迹")}</h3><p>{decision.matched_rule_name || t(routingLabel(decision.reason))}</p></div><Badge variant={decision.action === "REJECT" ? "danger" : decision.action === "DIRECT" ? "info" : "success"}>{t(routingLabel(decision.action))}</Badge></div>
    <div className="trace-list">
      {result.decision_trace.environment_bypass_matched ? <TraceRow name={t("环境直连规则")} reason="matched" matched /> : null}
      {result.decision_trace.rules.map((rule) => <TraceRow key={rule.id} name={rule.name} reason={rule.reason} matched={rule.matched} />)}
      {!result.decision_trace.environment_bypass_matched && result.decision_trace.rules.length === 0 ? <p className="muted">{t("没有需要检查的持久化规则")}</p> : null}
    </div>
  </section>;
}

function FallbackSection({ route, platformNames }: { route?: Route; platformNames: Map<string, string> }) {
  const { t } = useI18n();
  return <section className="card diagnostic-section">
    <div className="section-heading"><div><h3>{t("Platform fallback")}</h3><p>{route ? t("按配置顺序检查可用节点") : t("本次决策不经过 Platform")}</p></div></div>
    {route ? <div className="fallback-trace"><span>{route.root.name}</span>{route.fallback.filter((platform) => platform.id !== route.root.id).map((platform) => <span key={platform.id}><ArrowRight size={13} />{platform.name}</span>)}</div> : null}
    {route?.lease ? <dl className="compact-kv"><dt>{t("现有租约")}</dt><dd>{route.lease.egress_ip}</dd><dt>{t("租约 Platform")}</dt><dd>{platformNames.get(route.lease.selected_platform_id) || route.lease.selected_platform_id}</dd><dt>{t("轮换状态")}</dt><dd>{route.lease.rotation_deferred ? t(routingLabel(route.lease.rotation_deferred_reason)) : t("无需延期")}</dd></dl> : null}
  </section>;
}

function CandidateSection({ route, platformNames }: { route: Route; platformNames: Map<string, string> }) {
  const { t } = useI18n();
  const eligible = route.candidates.filter((item) => item.eligible).length;
  return <section className="card diagnostic-section diagnostic-wide">
    <div className="section-heading"><div><h3>{t("候选与排除")}</h3><p>{t("{{eligible}} / {{total}} 个节点可选", { eligible, total: route.candidates.length })}</p></div></div>
    <div className="diagnostic-table-wrap"><table className="diagnostic-table"><thead><tr><th>Platform</th><th>{t("节点")}</th><th>{t("出口 IP")}</th><th>{t("结果")}</th><th>{t("活跃租约 / 容量")}</th><th>{t("排除原因")}</th></tr></thead><tbody>{route.candidates.map((item) => <tr key={`${item.platform_id}-${item.node_hash}`}><td>{platformNames.get(item.platform_id) || item.platform_id}</td><td className="diagnostic-code" title={item.node_hash}>{shortHash(item.node_hash)}</td><td className="diagnostic-code">{item.egress_ip || "-"}</td><td><Badge variant={item.eligible ? "success" : "muted"}>{item.eligible ? t("可选") : t("已排除")}</Badge></td><td>{item.active_lease_count} / {item.max_active_leases || "∞"}</td><td className="diagnostic-errors">{item.exclusions.map((reason) => t(routingLabel(reason))).join("，") || "-"}</td></tr>)}</tbody></table></div>
  </section>;
}

function P2CSection({ route, platformNames }: { route: Route; platformNames: Map<string, string> }) {
  const { t } = useI18n();
  return <section className="card diagnostic-section"><div className="section-heading"><div><h3>{t("P2C 节点比较")}</h3><p>{route.p2c.length ? t("每个 Platform 抽取两个节点，选择分数更低者。") : t("候选不足，无需执行比较。")}</p></div></div>{route.p2c.map((item) => <div className="p2c-row" key={item.platform_id}><strong>{platformNames.get(item.platform_id) || item.platform_id}</strong><span className={item.trace.selected_hash === item.trace.first_node_hash ? "selected" : ""}>{shortHash(item.trace.first_node_hash)} · {formatScore(item.trace.first_score)}</span><span className={item.trace.selected_hash === item.trace.second_node_hash ? "selected" : ""}>{shortHash(item.trace.second_node_hash)} · {formatScore(item.trace.second_score)}</span></div>)}</section>;
}

function FinalSelectionSection({ route }: { route: Route }) {
  const { t } = useI18n();
  const selection = route.selection;
  return <section className="card diagnostic-section"><div className="section-heading"><div><h3>{t("最终选择")}</h3><p>{t(routingLabel(route.outcome))}</p></div></div>{selection ? <dl className="compact-kv"><dt>Platform</dt><dd>{selection.platform_name}</dd><dt>{t("出口 IP")}</dt><dd>{selection.egress_ip}</dd><dt>{t("节点")}</dt><dd className="diagnostic-code" title={selection.node_hash}>{shortHash(selection.node_hash)}</dd><dt>{t("选择原因")}</dt><dd>{t(routingLabel(selection.reason))}</dd><dt>{t("轮换结果")}</dt><dd>{selection.rotation_status ? t(routingLabel(selection.rotation_status)) : t("未触发轮换")} {selection.rotation_reason ? `· ${t(routingLabel(selection.rotation_reason))}` : ""}</dd></dl> : <p className="muted">{t("没有可用节点，请根据上方排除原因修复配置或节点状态。")}</p>}</section>;
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
