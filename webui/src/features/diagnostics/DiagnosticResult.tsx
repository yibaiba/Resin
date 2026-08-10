import { ArrowRight, Check, CircleX, GitBranch, Network, RadioTower } from "lucide-react";
import { Badge } from "../../components/ui/Badge";
import { useI18n } from "../../i18n";
import type { DiagnosticResponse } from "./types";

export function DiagnosticResult({ result }: { result: DiagnosticResponse }) {
  const { t } = useI18n();
  const route = result.route;
  return (
    <section className="diagnostic-result" aria-live="polite">
      <div className="diagnostic-outcome">
        <div><span className="diagnostic-step-icon"><GitBranch size={17} /></span><div><small>{t("决策")}</small><strong>{result.decision.action}</strong></div></div>
        <ArrowRight size={16} aria-hidden="true" />
        <div><span className="diagnostic-step-icon"><Network size={17} /></span><div><small>{t("选择")}</small><strong>{route?.selection?.egress_ip || route?.outcome || result.decision.reason}</strong></div></div>
        {result.live_tcp ? <><ArrowRight size={16} aria-hidden="true" /><div><span className="diagnostic-step-icon"><RadioTower size={17} /></span><div><small>LIVE TCP</small><strong className={result.live_tcp.success ? "text-success" : "text-danger"}>{result.live_tcp.attempted ? (result.live_tcp.success ? `${result.live_tcp.duration_ms} ms` : t("失败")) : result.live_tcp.reason}</strong></div></div></> : null}
      </div>

      <div className="diagnostic-grid">
        <section className="diagnostic-section">
          <div className="section-heading"><div><h3>{t("规则轨迹")}</h3><p>{result.decision.matched_rule_name || result.decision.reason}</p></div><Badge variant={result.decision.action === "REJECT" ? "danger" : result.decision.action === "DIRECT" ? "info" : "success"}>{result.decision.action}</Badge></div>
          <div className="trace-list">
            {result.decision_trace.environment_bypass_matched ? <TraceRow name={t("环境直连规则")} reason="matched" matched /> : null}
            {result.decision_trace.rules.map((rule) => <TraceRow key={rule.id} name={rule.name} reason={rule.reason} matched={rule.matched} />)}
            {!result.decision_trace.environment_bypass_matched && result.decision_trace.rules.length === 0 ? <p className="muted">{t("无持久化规则")}</p> : null}
          </div>
        </section>

        <section className="diagnostic-section">
          <div className="section-heading"><div><h3>Fallback</h3><p>{route ? `${t("Seed")} ${route.seed}` : "-"}</p></div></div>
          {route ? <div className="fallback-trace"><span>{route.root.name}</span>{route.fallback.filter((platform) => platform.id !== route.root.id).map((platform) => <span key={platform.id}><ArrowRight size={13} />{platform.name}</span>)}</div> : <p className="muted">{t("本次决策不经过 Platform")}</p>}
          {route?.lease ? <dl className="compact-kv"><dt>{t("现有租约")}</dt><dd>{route.lease.egress_ip}</dd><dt>{t("租约平台")}</dt><dd>{route.lease.selected_platform_id}</dd><dt>{t("轮换")}</dt><dd>{route.lease.rotation_deferred ? route.lease.rotation_deferred_reason : t("未延期")}</dd></dl> : null}
        </section>
      </div>

      {route ? <>
        <section className="diagnostic-section diagnostic-wide">
          <div className="section-heading"><div><h3>{t("候选与排除")}</h3><p>{t("{{eligible}} / {{total}} 可选", { eligible: route.candidates.filter((item) => item.eligible).length, total: route.candidates.length })}</p></div></div>
          <div className="diagnostic-table-wrap"><table className="diagnostic-table"><thead><tr><th>Platform</th><th>{t("节点")}</th><th>{t("出口 IP")}</th><th>{t("状态")}</th><th>{t("容量")}</th><th>{t("排除原因")}</th></tr></thead><tbody>{route.candidates.map((item) => <tr key={`${item.platform_id}-${item.node_hash}`}><td>{item.platform_id}</td><td className="diagnostic-code">{item.node_hash}</td><td className="diagnostic-code">{item.egress_ip || "-"}</td><td><Badge variant={item.eligible ? "success" : "muted"}>{item.eligible ? t("可选") : t("排除")}</Badge></td><td>{item.active_lease_count} / {item.max_active_leases || "∞"}</td><td className="diagnostic-errors">{item.exclusions.join(", ") || "-"}</td></tr>)}</tbody></table></div>
        </section>
        <div className="diagnostic-grid">
          <section className="diagnostic-section"><div className="section-heading"><div><h3>P2C</h3><p>{route.p2c.length ? t("评分比较") : t("未执行")}</p></div></div>{route.p2c.map((item) => <div className="p2c-row" key={item.platform_id}><strong>{item.platform_id}</strong><span className={item.trace.selected_hash === item.trace.first_node_hash ? "selected" : ""}>{shortHash(item.trace.first_node_hash)} · {formatScore(item.trace.first_score)}</span><span className={item.trace.selected_hash === item.trace.second_node_hash ? "selected" : ""}>{shortHash(item.trace.second_node_hash)} · {formatScore(item.trace.second_score)}</span></div>)}</section>
          <section className="diagnostic-section"><div className="section-heading"><div><h3>{t("最终选择")}</h3><p>{route.outcome}</p></div></div>{route.selection ? <dl className="compact-kv"><dt>Platform</dt><dd>{route.selection.platform_name}</dd><dt>{t("出口 IP")}</dt><dd>{route.selection.egress_ip}</dd><dt>{t("节点")}</dt><dd className="diagnostic-code">{route.selection.node_hash}</dd><dt>{t("原因")}</dt><dd>{route.selection.reason}</dd><dt>{t("轮换")}</dt><dd>{route.selection.rotation_status || "-"} {route.selection.rotation_reason || ""}</dd></dl> : <p className="muted">{t("没有可用选择")}</p>}</section>
        </div>
      </> : null}
      {result.live_tcp?.error ? <div className="callout callout-error diagnostic-errors"><CircleX size={15} /><span>{result.live_tcp.error}</span></div> : null}
    </section>
  );
}

function TraceRow({ name, reason, matched }: { name: string; reason: string; matched: boolean }) {
  return <div className={matched ? "trace-row trace-row-matched" : "trace-row"}>{matched ? <Check size={14} /> : <CircleX size={14} />}<strong>{name}</strong><span>{reason}</span></div>;
}

function shortHash(value?: string): string {
  if (!value) return "-";
  return value.length > 12 ? `${value.slice(0, 12)}...` : value;
}

function formatScore(value: number): string {
  return Number.isFinite(value) ? value.toFixed(3) : "-";
}
