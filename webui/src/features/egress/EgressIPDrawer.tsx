import { RotateCw, Save, X } from "lucide-react";
import { useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { useI18n } from "../../i18n";
import { routingLabel } from "../../lib/routing-labels";
import { rotationStrategies, rotationStrategyLabel } from "../platforms/constants";
import type { RotationStrategy } from "../platforms/types";
import type { EgressIP, EgressPolicyInput } from "./types";

type Props = {
  item: EgressIP;
  pending: boolean;
  onClose: () => void;
  onSave: (input: EgressPolicyInput) => void;
  onRotate: (strategy: RotationStrategy) => void;
};

function toLocalDateTime(value?: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}

export function EgressIPDrawer({ item, pending, onClose, onSave, onRotate }: Props) {
  const { t } = useI18n();
  const [status, setStatus] = useState(item.configured_status);
  const [capacity, setCapacity] = useState(String(item.max_active_leases));
  const [resumeAt, setResumeAt] = useState(toLocalDateTime(item.resume_at));
  const [strategy, setStrategy] = useState<RotationStrategy>("ANY");

  const capacityValue = Number(capacity);
  const capacityInvalid = !Number.isInteger(capacityValue) || capacityValue < 0;
  const badge = item.status === "ACTIVE" ? "success" : item.status === "DRAINING" ? "warning" : "danger";
  const save = () => onSave({
    status,
    max_active_leases: capacityValue,
    resume_at: resumeAt ? new Date(resumeAt).toISOString() : "",
  });

  return (
    <div className="drawer-overlay" role="dialog" aria-modal="true" aria-labelledby="egress-drawer-title" onClick={onClose}>
      <Card className="drawer-panel egress-drawer" onClick={(event) => event.stopPropagation()}>
        <div className="drawer-header">
          <div><h3 id="egress-drawer-title" className="egress-ip-value">{item.egress_ip}</h3><p>{t("编辑出口状态、容量和自动恢复时间。")}</p></div>
          <div className="drawer-header-actions"><Badge variant={badge}>{t(routingLabel(item.status))}</Badge><Button variant="ghost" size="sm" onClick={onClose} aria-label={t("关闭")}><X size={16} /></Button></div>
        </div>
        <div className="platform-drawer-layout">
          <section className="platform-drawer-section">
            <div className="platform-drawer-section-head"><h4>{t("出口策略")}</h4><p>{t("状态会影响新租约和现有粘性租约的下一次选择。")}</p></div>
            <dl className="egress-summary-grid">
              <div><dt>{t("关联节点")}</dt><dd>{item.node_count}</dd></div>
              <div><dt>{t("活跃租约")}</dt><dd>{item.max_active_leases === 0 ? `${item.active_lease_count} / ${t("不限")}` : `${item.active_lease_count} / ${item.max_active_leases}`}</dd></div>
              <div><dt>{t("覆盖 Platform")}</dt><dd>{item.platform_ids.length}</dd></div>
            </dl>
            <form className="form-grid egress-policy-form" onSubmit={(event) => { event.preventDefault(); save(); }}>
              <div className="field-group"><label className="field-label" htmlFor="egress-drawer-status">{t("运行状态")}</label><Select id="egress-drawer-status" value={status} onChange={(event) => setStatus(event.target.value as EgressIP["status"])}><option value="ACTIVE">{t("正常接收新租约")}</option><option value="DRAINING">{t("停止新分配并迁移现有租约")}</option><option value="BLOCKED">{t("立即禁止使用")}</option></Select></div>
              <div className="field-group"><label className="field-label" htmlFor="egress-drawer-capacity">{t("最大活跃租约")}</label><Input id="egress-drawer-capacity" type="number" min={0} value={capacity} invalid={capacityInvalid} aria-invalid={capacityInvalid} aria-describedby={capacityInvalid ? "egress-capacity-error" : undefined} onChange={(event) => setCapacity(event.target.value)} />{capacityInvalid ? <span id="egress-capacity-error" className="field-error" role="alert">{t("{{field}} 必须是非负整数", { field: t("最大活跃租约") })}</span> : null}<span className="field-help">{t("0 表示不限制此出口的并发粘性租约数。")}</span></div>
              <div className="field-group field-span-2"><label className="field-label" htmlFor="egress-drawer-resume">{t("自动恢复时间")}</label><Input id="egress-drawer-resume" type="datetime-local" value={resumeAt} disabled={status === "ACTIVE"} onChange={(event) => setResumeAt(event.target.value)} /><span className="field-help">{t("仅在排空或禁用时生效；留空表示不自动恢复。")}</span></div>
              <div className="detail-actions field-span-2"><Button type="submit" disabled={pending || capacityInvalid}><Save size={15} />{pending ? t("保存中...") : t("保存策略")}</Button></div>
            </form>
          </section>
          <section className="platform-drawer-section platform-ops-section">
            <div className="platform-drawer-section-head"><h4>{t("迁移现有租约")}</h4><p>{t("按选定策略重新选择该出口当前关联的粘性租约。")}</p></div>
            <div className="egress-rotation-controls"><Select value={strategy} onChange={(event) => setStrategy(event.target.value as RotationStrategy)} aria-label={t("轮换策略")}>{rotationStrategies.map((value) => <option key={value} value={value}>{t(rotationStrategyLabel[value])}</option>)}</Select><Button variant="secondary" onClick={() => onRotate(strategy)} disabled={pending || item.active_lease_count === 0}><RotateCw size={15} />{t("立即轮换")}</Button></div>
          </section>
        </div>
      </Card>
    </div>
  );
}
