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
  presentation: "modal" | "drawer";
  title: string;
  onChange: (next: RoutingRuleForm) => void;
  onClose: () => void;
  onSubmit: () => void;
};

type PatchForm = <Key extends keyof RoutingRuleForm>(key: Key, value: RoutingRuleForm[Key]) => void;

export function RoutingRuleEditor({ form, platforms, pending, presentation, title, onChange, onClose, onSubmit }: Props) {
  const { t } = useI18n();
  const patch: PatchForm = (key, value) => onChange({ ...form, [key]: value });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    onSubmit();
  };

  const content = (
    <>
      <div className={presentation === "drawer" ? "drawer-header" : "modal-header"}>
        <div><h3 id="routing-rule-editor-title">{t(title)}</h3><p>{t("先定义匹配范围，再决定命中后的动作。")}</p></div>
        <Button variant="ghost" size="sm" onClick={onClose} aria-label={t("关闭")} title={t("关闭")}><X size={16} /></Button>
      </div>
      <form className="route-rule-form form-grid" onSubmit={submit}>
          <div className="field-group route-rule-span-2">
            <label className="field-label" htmlFor="route-rule-name">{t("规则名称")}</label>
            <Input id="route-rule-name" value={form.name} placeholder={t("例如：拒绝内部域名")} onChange={(event) => patch("name", event.target.value)} autoFocus />
            <span className="field-help">{t("使用能说明目的的名称，排障时会显示在命中结果中。")}</span>
          </div>
          <RuleMatchFields form={form} patch={patch} />
          <RuleActionFields form={form} platforms={platforms} patch={patch} />
          <div className="route-rule-toggle-group route-rule-span-2">
            <label className="route-rule-toggle" htmlFor="route-rule-enabled">
              <span><strong>{t("启用规则")}</strong><small>{t("关闭后保留配置，但不参与匹配。")}</small></span>
              <Switch id="route-rule-enabled" checked={form.enabled} onChange={(event) => patch("enabled", event.target.checked)} />
            </label>
            <label className="route-rule-toggle" htmlFor="route-rule-override">
              <span><strong>{t("覆盖显式 Platform")}</strong><small>{t("开启后，即使客户端指定了 Platform，本规则仍可改写或拒绝。")}</small></span>
              <Switch id="route-rule-override" checked={form.overrideExplicitPlatform} onChange={(event) => patch("overrideExplicitPlatform", event.target.checked)} />
            </label>
          </div>
          <div className="route-rule-actions route-rule-span-2">
            <Button variant="secondary" onClick={onClose}>{t("取消")}</Button>
            <Button type="submit" disabled={pending}><Save size={15} />{pending ? t("保存中...") : t("保存规则")}</Button>
          </div>
      </form>
    </>
  );

  if (presentation === "drawer") {
    return (
      <div className="drawer-overlay" role="dialog" aria-modal="true" aria-labelledby="routing-rule-editor-title" onClick={onClose}>
        <Card className="drawer-panel route-rule-drawer" onClick={(event) => event.stopPropagation()}>{content}</Card>
      </div>
    );
  }

  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-labelledby="routing-rule-editor-title" onClick={onClose}>
      <Card className="modal-card route-rule-modal" onClick={(event) => event.stopPropagation()}>{content}</Card>
    </div>
  );
}

function RuleMatchFields({ form, patch }: { form: RoutingRuleForm; patch: PatchForm }) {
  const { t } = useI18n();
  const hostHelp = hostTypeHelp[form.hostType];
  return (
    <section className="route-rule-form-section route-rule-span-2" aria-labelledby="rule-match-heading">
      <div className="route-rule-form-section-head"><h4 id="rule-match-heading">{t("匹配条件")}</h4><p>{t("留空端口表示所有端口；所有条件必须同时满足。")}</p></div>
      <div className="route-rule-section-grid">
        <div className="field-group"><label className="field-label" htmlFor="route-rule-host-type">{t("主机匹配")}</label><Select id="route-rule-host-type" value={form.hostType} onChange={(event) => patch("hostType", event.target.value as RoutingRuleForm["hostType"])}>{hostTypeOptions.map((option) => <option key={option.value} value={option.value}>{t(option.label)}</option>)}</Select><span className="field-help">{t(hostHelp)}</span></div>
        {form.hostType !== "ANY" ? <div className="field-group"><label className="field-label" htmlFor="route-rule-host-value">{t("主机值")}</label><Input id="route-rule-host-value" value={form.hostValue} placeholder={hostTypePlaceholder[form.hostType]} onChange={(event) => patch("hostValue", event.target.value)} /><span className="field-help">{t("这里只填写主机或网段，不包含协议和端口。")}</span></div> : null}
        <div className="field-group"><label className="field-label" htmlFor="route-rule-ports">{t("端口范围")}</label><Input id="route-rule-ports" value={form.ports} placeholder="80, 443, 8000-8100" onChange={(event) => patch("ports", event.target.value)} /><span className="field-help">{t("多个端口用逗号分隔，例如 80, 443, 8000-8100。")}</span></div>
        <div className="field-group"><label className="field-label" htmlFor="route-rule-network">{t("网络")}</label><Select id="route-rule-network" value={form.network} onChange={(event) => patch("network", event.target.value as RoutingRuleForm["network"])}><option value="ANY">{t("TCP 与 UDP")}</option><option value="TCP">TCP</option><option value="UDP">UDP</option></Select><span className="field-help">{t("ANY 会同时匹配 TCP 与 UDP 请求。")}</span></div>
      </div>
    </section>
  );
}

function RuleActionFields({ form, platforms, patch }: { form: RoutingRuleForm; platforms: Platform[]; patch: PatchForm }) {
  const { t } = useI18n();
  return (
    <section className="route-rule-form-section route-rule-span-2" aria-labelledby="rule-action-heading">
      <div className="route-rule-form-section-head"><h4 id="rule-action-heading">{t("命中动作")}</h4><p>{t("第一条命中的规则会立即执行这个动作。")}</p></div>
      <div className="route-rule-section-grid">
        <div className="field-group"><label className="field-label" htmlFor="route-rule-action">{t("动作")}</label><Select id="route-rule-action" value={form.action} onChange={(event) => patch("action", event.target.value as RoutingRuleForm["action"])}><option value="PLATFORM">{t("指定 Platform")}</option><option value="DIRECT">{t("直接连接")}</option><option value="REJECT">{t("拒绝请求")}</option></Select><span className="field-help">{t(actionHelp[form.action])}</span></div>
        {form.action === "PLATFORM" ? <div className="field-group"><label className="field-label" htmlFor="route-rule-platform">Platform</label><Select id="route-rule-platform" value={form.platformID} onChange={(event) => patch("platformID", event.target.value)}><option value="">{t("选择平台")}</option>{platforms.map((platform) => <option key={platform.id} value={platform.id}>{platform.name}</option>)}</Select><span className="field-help">{t("命中后从该 Platform 及其 fallback 中选择节点。")}</span></div> : null}
      </div>
    </section>
  );
}

const hostTypeOptions = [
  { value: "ANY", label: "所有目标" },
  { value: "EXACT", label: "精确主机" },
  { value: "DOMAIN_SUFFIX", label: "域名后缀" },
  { value: "WILDCARD", label: "通配符" },
  { value: "CIDR", label: "IP 网段" },
] as const;

const hostTypeHelp: Record<RoutingRuleForm["hostType"], string> = {
  ANY: "匹配任意目标主机。",
  EXACT: "仅匹配完全相同的域名或 IP。",
  DOMAIN_SUFFIX: "匹配该域名及其所有子域名。",
  WILDCARD: "使用 * 和 ? 匹配主机名。",
  CIDR: "匹配指定 IPv4 或 IPv6 网段。",
};

const hostTypePlaceholder: Record<Exclude<RoutingRuleForm["hostType"], "ANY">, string> = {
  EXACT: "api.example.com",
  DOMAIN_SUFFIX: "example.com",
  WILDCARD: "*.example.com",
  CIDR: "10.0.0.0/8",
};

const actionHelp: Record<RoutingRuleForm["action"], string> = {
  PLATFORM: "交给选定 Platform 进行粘性选路。",
  DIRECT: "绕过节点池，由 Resin 所在主机直接连接。",
  REJECT: "在建立上游连接前拒绝请求。",
};
