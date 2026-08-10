import { RotateCw, Save } from "lucide-react";
import { useEffect, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { useI18n } from "../../i18n";
import { routingLabel } from "../../lib/routing-labels";
import { rotationStrategies, rotationStrategyLabel } from "../platforms/constants";
import type { RotationStrategy } from "../platforms/types";
import type { EgressIP, EgressPolicyInput } from "./types";

type Props = {
  item: EgressIP;
  checked: boolean;
  pending: boolean;
  onChecked: (checked: boolean) => void;
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

function toRFC3339(value: string): string {
  return value ? new Date(value).toISOString() : "";
}

export function EgressIPRow({ item, checked, pending, onChecked, onSave, onRotate }: Props) {
  const { t } = useI18n();
  const [status, setStatus] = useState(item.configured_status);
  const [capacity, setCapacity] = useState(String(item.max_active_leases));
  const [resumeAt, setResumeAt] = useState(toLocalDateTime(item.resume_at));
  const [strategy, setStrategy] = useState<RotationStrategy>("ANY");

  useEffect(() => {
    setStatus(item.configured_status);
    setCapacity(String(item.max_active_leases));
    setResumeAt(toLocalDateTime(item.resume_at));
  }, [item]);

  const badge = item.status === "ACTIVE" ? "success" : item.status === "DRAINING" ? "warning" : "danger";
  const capacityValue = Number(capacity);
  const capacityInvalid = !Number.isInteger(capacityValue) || capacityValue < 0;
  const save = () => onSave({ status, max_active_leases: capacityValue, resume_at: toRFC3339(resumeAt) });

  return (
    <article className="card egress-item">
      <header className="egress-item-header">
        <label className="egress-select">
          <input type="checkbox" checked={checked} onChange={(event) => onChecked(event.target.checked)} />
          <span className="sr-only">{t("选择出口 {{ip}}", { ip: item.egress_ip })}</span>
        </label>
        <div className="egress-item-title">
          <strong className="egress-ip-value">{item.egress_ip}</strong>
          <Badge variant={badge}>{t(routingLabel(item.status))}</Badge>
          {item.status !== item.configured_status ? <span className="egress-effective-note">{t("配置等待生效：{{status}}", { status: t(routingLabel(item.configured_status)) })}</span> : null}
        </div>
      </header>

      <div className="egress-metrics" aria-label={t("出口概况")}>
        <Metric label={t("关联节点")} value={String(item.node_count)} />
        <Metric label={t("活跃租约")} value={item.max_active_leases === 0 ? `${item.active_lease_count} / ${t("不限")}` : `${item.active_lease_count} / ${item.max_active_leases}`} />
        <Metric label={t("覆盖 Platform")} value={String(item.platform_ids.length)} />
        <Metric label={t("自动恢复")} value={item.resume_at ? new Date(item.resume_at).toLocaleString() : t("未设置")} />
      </div>

      <div className="egress-policy-grid">
        <div className="field-group">
          <label className="field-label" htmlFor={`egress-status-${item.egress_ip}`}>{t("运行状态")}</label>
          <Select id={`egress-status-${item.egress_ip}`} value={status} onChange={(event) => setStatus(event.target.value as EgressIP["status"])}>
            <option value="ACTIVE">{t("正常接收新租约")}</option>
            <option value="DRAINING">{t("停止新分配并迁移现有租约")}</option>
            <option value="BLOCKED">{t("立即禁止使用")}</option>
          </Select>
          <span className="field-help">{t("状态会影响新租约和现有粘性租约的下一次选择。")}</span>
        </div>
        <div className="field-group">
          <label className="field-label" htmlFor={`egress-capacity-${item.egress_ip}`}>{t("最大活跃租约")}</label>
          <Input id={`egress-capacity-${item.egress_ip}`} type="number" min={0} value={capacity} invalid={capacityInvalid} onChange={(event) => setCapacity(event.target.value)} />
          <span className="field-help">{t("0 表示不限制此出口的并发粘性租约数。")}</span>
        </div>
        <div className="field-group">
          <label className="field-label" htmlFor={`egress-resume-${item.egress_ip}`}>{t("自动恢复时间")}</label>
          <Input id={`egress-resume-${item.egress_ip}`} type="datetime-local" value={resumeAt} disabled={status === "ACTIVE"} onChange={(event) => setResumeAt(event.target.value)} />
          <span className="field-help">{t("仅在排空或禁用时生效；留空表示不自动恢复。")}</span>
        </div>
        <Button className="egress-save" variant="secondary" onClick={save} disabled={pending || capacityInvalid}><Save size={15} />{t("保存策略")}</Button>
      </div>

      <div className="egress-rotation-row">
        <div><strong>{t("迁移现有租约")}</strong><p>{t("按选定策略重新选择该出口当前关联的粘性租约。")}</p></div>
        <Select value={strategy} onChange={(event) => setStrategy(event.target.value as RotationStrategy)} aria-label={t("轮换策略")}>
          {rotationStrategies.map((value) => <option key={value} value={value}>{t(rotationStrategyLabel[value])}</option>)}
        </Select>
        <Button variant="secondary" onClick={() => onRotate(strategy)} disabled={pending || item.active_lease_count === 0}><RotateCw size={15} />{t("立即轮换")}</Button>
      </div>
    </article>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return <div><span>{label}</span><strong>{value}</strong></div>;
}
