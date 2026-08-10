import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { Button } from "../../components/ui/Button";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { useI18n } from "../../i18n";
import { rotationStrategies, rotationStrategyDescription, rotationStrategyLabel } from "./constants";
import type { Platform, RotationStrategy } from "./types";

type Props = {
  platformID: string;
  platforms: Platform[];
  fallbackIDs: string[];
  rotationInterval: string;
  rotationStrategy: RotationStrategy;
  onFallbackChange: (ids: string[]) => void;
  onRotationIntervalChange: (value: string) => void;
  onRotationStrategyChange: (value: RotationStrategy) => void;
};

export function PlatformRoutingSettings(props: Props) {
  const { t } = useI18n();
  const [candidate, setCandidate] = useState("");
  const platformNames = useMemo(() => new Map(props.platforms.map((item) => [item.id, item.name])), [props.platforms]);
  const available = props.platforms.filter((item) => item.id !== props.platformID && !props.fallbackIDs.includes(item.id));
  const add = () => {
    if (!candidate) return;
    props.onFallbackChange([...props.fallbackIDs, candidate]);
    setCandidate("");
  };
  const move = (index: number, delta: number) => {
    const target = index + delta;
    if (target < 0 || target >= props.fallbackIDs.length) return;
    const next = [...props.fallbackIDs];
    [next[index], next[target]] = [next[target], next[index]];
    props.onFallbackChange(next);
  };

  return (
    <div className="platform-routing-settings route-rule-span-2">
      <div className="platform-config-subsection">
        <div className="section-heading"><div><h5>{t("Platform fallback")}</h5><p>{t("仅当当前 Platform 没有可用节点时，才按顺序尝试后备 Platform；连接失败后不会重放请求。")}</p></div></div>
        <div className="fallback-add-row">
          <Select value={candidate} onChange={(event) => setCandidate(event.target.value)} aria-label={t("选择 fallback 平台")}>
            <option value="">{available.length ? t("选择平台") : t("没有可添加的平台")}</option>
            {available.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
          </Select>
          <Button variant="secondary" size="sm" onClick={add} disabled={!candidate}><Plus size={15} />{t("添加")}</Button>
        </div>
        {props.fallbackIDs.length === 0 ? <div className="fallback-empty"><strong>{t("未配置后备 Platform")}</strong><p>{t("当前 Platform 无可用节点时，请求会直接失败。")}</p></div> : null}
        <ol className="fallback-list">
          {props.fallbackIDs.map((id, index) => (
            <li key={id}>
              <span className="fallback-order">{index + 1}</span>
              <span className="fallback-name">{platformNames.get(id) ?? id}</span>
              <div className="fallback-actions">
                <Button variant="ghost" size="sm" onClick={() => move(index, -1)} disabled={index === 0} title={t("上移")} aria-label={t("上移")}><ArrowUp size={14} /></Button>
                <Button variant="ghost" size="sm" onClick={() => move(index, 1)} disabled={index === props.fallbackIDs.length - 1} title={t("下移")} aria-label={t("下移")}><ArrowDown size={14} /></Button>
                <Button variant="ghost" size="sm" onClick={() => props.onFallbackChange(props.fallbackIDs.filter((item) => item !== id))} title={t("移除")} aria-label={t("移除")}><Trash2 size={14} /></Button>
              </div>
            </li>
          ))}
        </ol>
      </div>
      <div className="platform-config-subsection rotation-config-grid">
        <div className="section-heading route-rule-span-2"><div><h5>{t("粘性轮换")}</h5><p>{t("Account 租约使用达到间隔后，在下一次请求时按策略重新选择。")}</p></div></div>
        <div className="field-group"><label className="field-label" htmlFor="detail-edit-rotation-interval">{t("轮换间隔")}</label><Input id="detail-edit-rotation-interval" value={props.rotationInterval} placeholder="30m" onChange={(event) => props.onRotationIntervalChange(event.target.value)} /><span className="field-help">{t("例如 30m 或 6h；0s 关闭定时轮换，且间隔必须小于粘性 TTL。")}</span></div>
        <div className="field-group"><label className="field-label" htmlFor="detail-edit-rotation-strategy">{t("轮换策略")}</label><Select id="detail-edit-rotation-strategy" value={props.rotationStrategy} onChange={(event) => props.onRotationStrategyChange(event.target.value as RotationStrategy)}>{rotationStrategies.map((strategy) => <option key={strategy} value={strategy}>{t(rotationStrategyLabel[strategy])}</option>)}</Select><span className="field-help">{t(rotationStrategyDescription[props.rotationStrategy])}</span></div>
      </div>
    </div>
  );
}
