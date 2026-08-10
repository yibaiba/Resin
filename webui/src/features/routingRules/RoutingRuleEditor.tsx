import { Save, X } from "lucide-react";
import { type FormEvent } from "react";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { Switch } from "../../components/ui/Switch";
import { useI18n } from "../../i18n";
import type { Platform } from "../platforms/types";
import type { RoutingRuleForm } from "./ruleForm";

type Props = {
  form: RoutingRuleForm;
  platforms: Platform[];
  pending: boolean;
  title: string;
  onChange: (next: RoutingRuleForm) => void;
  onClose: () => void;
  onSubmit: () => void;
};

export function RoutingRuleEditor({ form, platforms, pending, title, onChange, onClose, onSubmit }: Props) {
  const { t } = useI18n();
  const patch = <Key extends keyof RoutingRuleForm>(key: Key, value: RoutingRuleForm[Key]) => {
    onChange({ ...form, [key]: value });
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    onSubmit();
  };

  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-labelledby="routing-rule-editor-title">
      <Card className="modal-card route-rule-modal">
        <div className="modal-header">
          <h3 id="routing-rule-editor-title">{t(title)}</h3>
          <Button variant="ghost" size="sm" onClick={onClose} aria-label={t("关闭")} title={t("关闭")}>
            <X size={16} />
          </Button>
        </div>
        <form className="route-rule-form" onSubmit={submit}>
          <div className="field-group route-rule-span-2">
            <label className="field-label" htmlFor="route-rule-name">{t("规则名称")}</label>
            <Input id="route-rule-name" value={form.name} onChange={(event) => patch("name", event.target.value)} autoFocus />
          </div>
          <div className="field-group">
            <label className="field-label" htmlFor="route-rule-host-type">{t("主机匹配")}</label>
            <Select id="route-rule-host-type" value={form.hostType} onChange={(event) => patch("hostType", event.target.value as RoutingRuleForm["hostType"])}>
              <option value="ANY">ANY</option><option value="EXACT">EXACT</option>
              <option value="DOMAIN_SUFFIX">DOMAIN_SUFFIX</option><option value="WILDCARD">WILDCARD</option>
              <option value="CIDR">CIDR</option>
            </Select>
          </div>
          <div className="field-group">
            <label className="field-label" htmlFor="route-rule-host-value">{t("主机值")}</label>
            <Input id="route-rule-host-value" value={form.hostValue} disabled={form.hostType === "ANY"} placeholder="api.example.com" onChange={(event) => patch("hostValue", event.target.value)} />
          </div>
          <div className="field-group">
            <label className="field-label" htmlFor="route-rule-ports">{t("端口范围")}</label>
            <Input id="route-rule-ports" value={form.ports} placeholder="80, 443, 8000-8100" onChange={(event) => patch("ports", event.target.value)} />
          </div>
          <div className="field-group">
            <label className="field-label" htmlFor="route-rule-network">{t("网络")}</label>
            <Select id="route-rule-network" value={form.network} onChange={(event) => patch("network", event.target.value as RoutingRuleForm["network"])}>
              <option value="ANY">ANY</option><option value="TCP">TCP</option><option value="UDP">UDP</option>
            </Select>
          </div>
          <div className="field-group">
            <label className="field-label" htmlFor="route-rule-action">{t("动作")}</label>
            <Select id="route-rule-action" value={form.action} onChange={(event) => patch("action", event.target.value as RoutingRuleForm["action"])}>
              <option value="PLATFORM">PLATFORM</option><option value="DIRECT">DIRECT</option><option value="REJECT">REJECT</option>
            </Select>
          </div>
          <div className="field-group">
            <label className="field-label" htmlFor="route-rule-platform">Platform</label>
            <Select id="route-rule-platform" value={form.platformID} disabled={form.action !== "PLATFORM"} onChange={(event) => patch("platformID", event.target.value)}>
              <option value="">{t("选择平台")}</option>
              {platforms.map((platform) => <option key={platform.id} value={platform.id}>{platform.name}</option>)}
            </Select>
          </div>
          <label className="route-rule-toggle" htmlFor="route-rule-enabled">
            <span>{t("启用规则")}</span>
            <Switch id="route-rule-enabled" checked={form.enabled} onChange={(event) => patch("enabled", event.target.checked)} />
          </label>
          <label className="route-rule-toggle" htmlFor="route-rule-override">
            <span>{t("覆盖显式平台")}</span>
            <Switch id="route-rule-override" checked={form.overrideExplicitPlatform} onChange={(event) => patch("overrideExplicitPlatform", event.target.checked)} />
          </label>
          <div className="route-rule-actions route-rule-span-2">
            <Button variant="secondary" onClick={onClose}>{t("取消")}</Button>
            <Button type="submit" disabled={pending}><Save size={15} />{pending ? t("保存中...") : t("保存规则")}</Button>
          </div>
        </form>
      </Card>
    </div>
  );
}
