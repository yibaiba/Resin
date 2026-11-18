import { ArrowDown, ArrowUp, Trash2 } from "lucide-react";
import { useMemo } from "react";
import { Button } from "../../components/ui/Button";
import { Input } from "../../components/ui/Input";
import { Listbox } from "../../components/ui/Listbox";
import { Select } from "../../components/ui/Select";
import { useI18n } from "../../i18n";
import { rotationStrategies, rotationStrategyDescription, rotationStrategyLabel } from "./constants";
import type { Platform, RotationStrategy } from "./types";

const MAX_CHAIN_PLATFORM_IDS = 8;

type OrderedListProps = {
  idPrefix: string;
  platformID?: string;
  platforms: Platform[];
  ids: string[];
  onChange: (ids: string[]) => void;
  headingId?: string;
  heading: string;
  hint: string;
  addLabel: string;
  selectLabel: string;
  emptyTitle: string;
  emptyHint: string;
  orderLabel: string;
  max?: number;
};

export function OrderedPlatformList(props: OrderedListProps) {
  const { t } = useI18n();
  const ids = props.ids ?? [];
  const platformNames = useMemo(() => new Map(props.platforms.map((item) => [item.id, item.name])), [props.platforms]);
  const atMax = typeof props.max === "number" && ids.length >= props.max;
  const choices = props.platforms.filter((item) => item.id !== props.platformID);
  const options = choices.map((item) => ({
    value: item.id,
    label: item.name,
    disabled: atMax && !ids.includes(item.id),
  }));
  const move = (index: number, delta: number) => {
    const target = index + delta;
    if (target < 0 || target >= ids.length) return;
    const next = [...ids];
    [next[index], next[target]] = [next[target], next[index]];
    props.onChange(next);
  };

  return (
    <>
      <div id={props.headingId} className="routing-settings-heading field-span-2 routing-anchor-target">
        <h5>{props.heading}</h5>
        <p>{props.hint}</p>
      </div>
      <div className="field-group field-span-2">
        <label className="field-label" htmlFor={`${props.idPrefix}-platform`}>{props.addLabel}</label>
        <Listbox
          id={`${props.idPrefix}-platform`}
          multiple
          value={ids}
          onChange={props.onChange}
          aria-label={props.selectLabel}
          disabled={choices.length === 0}
          placeholder={choices.length ? t("点选可多选，已选项会显示在下方") : t("没有可添加的平台")}
          options={options}
        />
      </div>
      <div className="field-group field-span-2 fallback-order-field">
        <span className="field-label">{props.orderLabel}</span>
        {ids.length === 0 ? <div className="fallback-empty"><strong>{props.emptyTitle}</strong><p>{props.emptyHint}</p></div> : null}
        <ol className="fallback-list">
          {ids.map((id, index) => (
            <li key={id}>
              <span className="fallback-order">{index + 1}</span>
              <span className="fallback-name">{platformNames.get(id) ?? id}</span>
              <div className="fallback-actions">
                <Button variant="ghost" size="sm" onClick={() => move(index, -1)} disabled={index === 0} title={t("上移")} aria-label={t("上移")}><ArrowUp size={14} /></Button>
                <Button variant="ghost" size="sm" onClick={() => move(index, 1)} disabled={index === ids.length - 1} title={t("下移")} aria-label={t("下移")}><ArrowDown size={14} /></Button>
                <Button variant="ghost" size="sm" onClick={() => props.onChange(ids.filter((item) => item !== id))} title={t("移除")} aria-label={t("移除")}><Trash2 size={14} /></Button>
              </div>
            </li>
          ))}
        </ol>
      </div>
    </>
  );
}

type Props = {
  platformID: string;
  platforms: Platform[];
  chainIDs: string[];
  fallbackIDs: string[];
  rotationInterval: string;
  rotationStrategy: RotationStrategy;
  onChainChange: (ids: string[]) => void;
  onFallbackChange: (ids: string[]) => void;
  onRotationIntervalChange: (value: string) => void;
  onRotationStrategyChange: (value: RotationStrategy) => void;
};

export function PlatformRoutingSettings(props: Props) {
  const { t } = useI18n();

  return (
    <>
      <OrderedPlatformList
        idPrefix="detail-edit-chain"
        headingId="platform-chain"
        platformID={props.platformID}
        platforms={props.platforms}
        ids={props.chainIDs}
        onChange={props.onChainChange}
        heading={t("链式代理")}
        hint={t("流量按顺序先走这些前置 Platform，再从本平台节点落地。空着就是直连。前置节点不会并进本平台；本平台只用允许的订阅和过滤规则里的节点。")}
        addLabel={t("添加前置 Platform")}
        selectLabel={t("选择前置平台")}
        emptyTitle={t("未配置前置 Platform")}
        emptyHint={t("当前 Platform 直接从本平台节点出站。")}
        orderLabel={t("前置顺序")}
        max={MAX_CHAIN_PLATFORM_IDS}
      />
      <OrderedPlatformList
        idPrefix="detail-edit-fallback"
        headingId="platform-fallback"
        platformID={props.platformID}
        platforms={props.platforms}
        ids={props.fallbackIDs}
        onChange={props.onFallbackChange}
        heading={t("Platform fallback")}
        hint={t("仅当当前 Platform 没有可用节点时，才按顺序尝试后备 Platform；连接失败后不会重放请求。")}
        addLabel={t("添加后备 Platform")}
        selectLabel={t("选择 fallback 平台")}
        emptyTitle={t("未配置后备 Platform")}
        emptyHint={t("当前 Platform 无可用节点时，请求会直接失败。")}
        orderLabel={t("回退顺序")}
      />
      <div id="sticky-rotation" className="routing-settings-heading field-span-2 routing-anchor-target">
        <h5>{t("粘性轮换")}</h5>
        <p>{t("Account 租约使用达到间隔后，在下一次请求时按策略重新选择。")}</p>
      </div>
      <div className="field-group"><label className="field-label" htmlFor="detail-edit-rotation-interval">{t("轮换间隔")}</label><Input id="detail-edit-rotation-interval" value={props.rotationInterval} placeholder="30m" onChange={(event) => props.onRotationIntervalChange(event.target.value)} /><span className="field-help">{t("例如 30m 或 6h；0s 关闭定时轮换，且间隔必须小于粘性 TTL。")}</span></div>
      <div className="field-group"><label className="field-label" htmlFor="detail-edit-rotation-strategy">{t("轮换策略")}</label><Select id="detail-edit-rotation-strategy" value={props.rotationStrategy} onChange={(event) => props.onRotationStrategyChange(event.target.value as RotationStrategy)}>{rotationStrategies.map((strategy) => <option key={strategy} value={strategy}>{t(rotationStrategyLabel[strategy])}</option>)}</Select><span className="field-help">{t(rotationStrategyDescription[props.rotationStrategy])}</span></div>
    </>
  );
}
