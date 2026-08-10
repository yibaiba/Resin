import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ArrowDown, ArrowUp, Pencil, Play, Plus, RefreshCw, Route, Trash2 } from "lucide-react";
import { useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { Switch } from "../../components/ui/Switch";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { listPlatforms } from "../platforms/api";
import { createRoutingRule, deleteRoutingRule, listRoutingRules, previewRoutingDecision, reorderRoutingRules, updateRoutingRule } from "./api";
import { emptyRoutingRuleForm, formatPortRanges, routingRuleFormToInput, routingRuleToForm, type RoutingRuleForm } from "./ruleForm";
import { RoutingRuleEditor } from "./RoutingRuleEditor";
import type { RoutingDecision, RoutingRule } from "./types";

export function RoutingRulesPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [editing, setEditing] = useState<RoutingRule | "new" | null>(null);
  const [form, setForm] = useState<RoutingRuleForm>(emptyRoutingRuleForm);
  const [preview, setPreview] = useState({ target: "", platform_name: "", network: "TCP" });
  const [decision, setDecision] = useState<RoutingDecision | null>(null);
  const rulesQuery = useQuery({ queryKey: ["routing-rules"], queryFn: listRoutingRules });
  const platformsQuery = useQuery({ queryKey: ["platforms", "routing-choices"], queryFn: () => listPlatforms({ limit: 1000 }) });
  const rules = rulesQuery.data ?? [];
  const platforms = platformsQuery.data?.items ?? [];
  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["routing-rules"] });
  const saveMutation = useMutation({
    mutationFn: () => editing === "new" ? createRoutingRule(routingRuleFormToInput(form)) : updateRoutingRule(editing!.id, routingRuleFormToInput(form)),
    onSuccess: async () => { await invalidate(); setEditing(null); showToast("success", t("路由规则已保存")); },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const toggleMutation = useMutation({
    mutationFn: (rule: RoutingRule) => updateRoutingRule(rule.id, { enabled: !rule.enabled }),
    onSuccess: invalidate,
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const deleteMutation = useMutation({
    mutationFn: deleteRoutingRule,
    onSuccess: async () => { await invalidate(); showToast("success", t("路由规则已删除")); },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const orderMutation = useMutation({
    mutationFn: reorderRoutingRules,
    onSuccess: invalidate,
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const previewMutation = useMutation({
    mutationFn: () => previewRoutingDecision(preview),
    onSuccess: setDecision,
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const openEditor = (rule?: RoutingRule) => {
    setForm(rule ? routingRuleToForm(rule) : { ...emptyRoutingRuleForm, platformID: platforms[0]?.id ?? "" });
    setEditing(rule ?? "new");
  };
  const move = (index: number, delta: number) => {
    const target = index + delta;
    if (target < 0 || target >= rules.length) return;
    const ids = rules.map((rule) => rule.id);
    [ids[index], ids[target]] = [ids[target], ids[index]];
    orderMutation.mutate(ids);
  };
  const remove = (rule: RoutingRule) => {
    if (window.confirm(t("确认删除路由规则 {{name}}？", { name: rule.name }))) deleteMutation.mutate(rule.id);
  };

  return (
    <section className="routing-control-page">
      <header className="module-header">
        <div><h2>{t("目标路由")}</h2><p className="module-description">{t("按优先级决定目标走平台、直连或拒绝。")}</p></div>
        <div className="module-actions">
          <Button variant="secondary" size="sm" onClick={() => void rulesQuery.refetch()} disabled={rulesQuery.isFetching}><RefreshCw size={15} className={rulesQuery.isFetching ? "spin" : undefined} />{t("刷新")}</Button>
          <Button size="sm" onClick={() => openEditor()}><Plus size={15} />{t("新建规则")}</Button>
        </div>
      </header>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      {rulesQuery.isError ? <div className="callout callout-error"><AlertTriangle size={15} /><span>{formatApiErrorMessage(rulesQuery.error, t)}</span></div> : null}
      <div className="route-rule-layout">
        <section className="route-rule-list" aria-label={t("路由规则列表")}>
          <div className="section-heading"><div><h3>{t("优先级队列")}</h3><p>{t("共 {{count}} 条", { count: rules.length })}</p></div></div>
          {rulesQuery.isLoading ? <p className="muted route-rule-state">{t("正在加载规则...")}</p> : null}
          {!rulesQuery.isLoading && rules.length === 0 ? <div className="empty-box"><Route size={18} /><p>{t("暂无目标路由规则")}</p></div> : null}
          <div className="route-rule-rows">
            {rules.map((rule, index) => (
              <article className="route-rule-row" key={rule.id}>
                <span className="route-rule-priority" aria-label={t("优先级 {{value}}", { value: index + 1 })}>{index + 1}</span>
                <div className="route-rule-summary">
                  <div className="route-rule-title"><strong>{rule.name}</strong><Badge variant={rule.action === "REJECT" ? "danger" : rule.action === "DIRECT" ? "info" : "accent"}>{rule.action}</Badge>{rule.override_explicit_platform ? <Badge variant="warning">{t("覆盖显式平台")}</Badge> : null}</div>
                  <p><code>{rule.host_type === "ANY" ? "*" : rule.host_value}</code><span>{rule.network}</span><span>{formatPortRanges(rule.ports) || t("所有端口")}</span></p>
                </div>
                <div className="route-rule-controls">
                  <Switch checked={rule.enabled} onChange={() => toggleMutation.mutate(rule)} aria-label={t("启用规则 {{name}}", { name: rule.name })} />
                  <Button variant="ghost" size="sm" onClick={() => move(index, -1)} disabled={index === 0 || orderMutation.isPending} title={t("上移")} aria-label={t("上移")}><ArrowUp size={15} /></Button>
                  <Button variant="ghost" size="sm" onClick={() => move(index, 1)} disabled={index === rules.length - 1 || orderMutation.isPending} title={t("下移")} aria-label={t("下移")}><ArrowDown size={15} /></Button>
                  <Button variant="ghost" size="sm" onClick={() => openEditor(rule)} title={t("编辑")} aria-label={t("编辑")}><Pencil size={15} /></Button>
                  <Button variant="ghost" size="sm" onClick={() => remove(rule)} title={t("删除")} aria-label={t("删除")}><Trash2 size={15} /></Button>
                </div>
              </article>
            ))}
          </div>
        </section>
        <Card className="route-preview-panel">
          <div className="section-heading"><div><h3>{t("规则预览")}</h3><p>{t("查看目标最终命中的动作。")}</p></div></div>
          <div className="field-group"><label className="field-label" htmlFor="route-preview-target">{t("目标地址")}</label><Input id="route-preview-target" value={preview.target} placeholder="api.example.com:443" onChange={(event) => setPreview({ ...preview, target: event.target.value })} /></div>
          <div className="field-group"><label className="field-label" htmlFor="route-preview-platform">{t("显式平台")}</label><Select id="route-preview-platform" value={preview.platform_name} onChange={(event) => setPreview({ ...preview, platform_name: event.target.value })}><option value="">{t("无")}</option>{platforms.map((platform) => <option key={platform.id} value={platform.name}>{platform.name}</option>)}</Select></div>
          <div className="field-group"><label className="field-label" htmlFor="route-preview-network">{t("网络")}</label><Select id="route-preview-network" value={preview.network} onChange={(event) => setPreview({ ...preview, network: event.target.value })}><option>TCP</option><option>UDP</option></Select></div>
          <Button className="w-full" onClick={() => previewMutation.mutate()} disabled={!preview.target.trim() || previewMutation.isPending}><Play size={15} />{previewMutation.isPending ? t("测试中...") : t("执行预览")}</Button>
          {decision ? <div className="route-preview-result"><div><Badge variant={decision.action === "REJECT" ? "danger" : decision.action === "DIRECT" ? "info" : "success"}>{decision.action}</Badge><strong>{decision.matched_rule_name || t("默认路由")}</strong></div><dl><dt>{t("原因")}</dt><dd>{decision.reason}</dd><dt>Platform</dt><dd>{decision.platform_name || "-"}</dd><dt>{t("规范目标")}</dt><dd>{decision.target.host}:{decision.target.port || "*"}</dd></dl></div> : null}
        </Card>
      </div>
      {editing ? <RoutingRuleEditor form={form} platforms={platforms} pending={saveMutation.isPending} title={editing === "new" ? "新建路由规则" : "编辑路由规则"} onChange={setForm} onClose={() => setEditing(null)} onSubmit={() => saveMutation.mutate()} /> : null}
    </section>
  );
}
