import { RotateCw, Save } from "lucide-react";
import { useEffect, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { useI18n } from "../../i18n";
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
  const save = () => onSave({ status, max_active_leases: Number(capacity), resume_at: toRFC3339(resumeAt) });

  return (
    <tr>
      <td><input type="checkbox" checked={checked} onChange={(event) => onChecked(event.target.checked)} aria-label={t("选择出口 {{ip}}", { ip: item.egress_ip })} /></td>
      <td><strong className="egress-ip-value">{item.egress_ip}</strong><small>{t("{{count}} 个节点", { count: item.node_count })}</small></td>
      <td><Badge variant={badge}>{item.status}</Badge>{item.status !== item.configured_status ? <small>{item.configured_status}</small> : null}</td>
      <td><strong>{item.active_lease_count}</strong><small>{item.max_active_leases === 0 ? t("无限制") : `/ ${item.max_active_leases}`}</small></td>
      <td><Select value={status} onChange={(event) => setStatus(event.target.value as EgressIP["status"])}><option>ACTIVE</option><option>DRAINING</option><option>BLOCKED</option></Select></td>
      <td><Input type="number" min={0} value={capacity} onChange={(event) => setCapacity(event.target.value)} aria-label={t("最大活跃租约")} /></td>
      <td><Input type="datetime-local" value={resumeAt} disabled={status === "ACTIVE"} onChange={(event) => setResumeAt(event.target.value)} aria-label={t("恢复时间")} /></td>
      <td>
        <div className="egress-row-actions">
          <Button variant="secondary" size="sm" onClick={save} disabled={pending || !Number.isInteger(Number(capacity)) || Number(capacity) < 0} title={t("保存配置")} aria-label={t("保存配置")}><Save size={14} /></Button>
          <Select value={strategy} onChange={(event) => setStrategy(event.target.value as RotationStrategy)} aria-label={t("轮换策略")}><option>ANY</option><option>SAME_IP</option><option>PREFER_NEW_IP</option><option>REQUIRE_NEW_IP</option></Select>
          <Button variant="secondary" size="sm" onClick={() => onRotate(strategy)} disabled={pending || item.active_lease_count === 0} title={t("轮换关联租约")} aria-label={t("轮换关联租约")}><RotateCw size={14} /></Button>
        </div>
      </td>
    </tr>
  );
}
