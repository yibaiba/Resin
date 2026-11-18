import { useQuery } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { Button } from "../../components/ui/Button";
import { Input } from "../../components/ui/Input";
import { Listbox } from "../../components/ui/Listbox";
import { useI18n } from "../../i18n";
import { listAllSubscriptions } from "../subscriptions/api";

type Props = {
  allowIDs: string[];
  excludeIDs: string[];
  create?: boolean;
  error?: string;
  onAllowChange: (ids: string[]) => void;
  onExcludeChange: (ids: string[]) => void;
};

export function PlatformSubscriptionPicker(props: Props) {
  const { t } = useI18n();
  const [keyword, setKeyword] = useState("");
  const allowIDs = props.allowIDs ?? [];
  const excludeIDs = props.excludeIDs ?? [];
  const prefix = props.create ? "create" : "detail-edit";

  const subscriptionsQuery = useQuery({
    queryKey: ["subscriptions", "platform-picker"],
    queryFn: () => listAllSubscriptions(),
  });
  const subscriptions = subscriptionsQuery.data ?? [];
  const names = useMemo(() => new Map(subscriptions.map((item) => [item.id, item])), [subscriptions]);
  const needle = keyword.trim().toLowerCase();
  const selected = useMemo(() => new Set([...allowIDs, ...excludeIDs]), [allowIDs, excludeIDs]);
  const visible = subscriptions
    .filter((item) => {
      if (selected.has(item.id)) {
        return true;
      }
      return !needle || item.name.toLowerCase().includes(needle) || item.id.toLowerCase().includes(needle);
    })
    .slice()
    .sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" }));

  return (
    <>
      <div className="routing-settings-heading field-span-2">
        <h5>{t("订阅过滤")}</h5>
        <p>
          {t("允许列表为空表示不限制来源；排除订阅会先去掉这些订阅。同一订阅不能同时出现在两边。")}
        </p>
      </div>
      <div className="field-group field-span-2">
        <label className="field-label" htmlFor={`${prefix}-subscription-search`}>
          {t("搜索订阅")}
        </label>
        <Input
          id={`${prefix}-subscription-search`}
          value={keyword}
          placeholder={t("按名称筛选订阅")}
          onChange={(event) => setKeyword(event.target.value)}
        />
      </div>
      <SubscriptionList
        id={`${prefix}-subscription-allow`}
        heading={t("允许的订阅")}
        selectedLabel={t("已允许")}
        emptyTitle={t("未限制订阅")}
        emptyHint={t("当前平台会接收所有未被排除的订阅中符合其他过滤规则的节点。")}
        placeholder={t("点选加入允许列表")}
        selectedIDs={allowIDs}
        blockedIDs={excludeIDs}
        subscriptions={visible}
        names={names}
        loading={subscriptionsQuery.isLoading && subscriptions.length === 0}
        onChange={props.onAllowChange}
      />
      <SubscriptionList
        id={`${prefix}-subscription-exclude`}
        heading={t("排除订阅")}
        selectedLabel={t("已排除")}
        emptyTitle={t("未排除订阅")}
        emptyHint={t("不排除任何订阅。家宽订阅可以只写在这里，Default 就不会再用这些节点。")}
        placeholder={t("点选加入排除列表")}
        selectedIDs={excludeIDs}
        blockedIDs={allowIDs}
        subscriptions={visible}
        names={names}
        loading={subscriptionsQuery.isLoading && subscriptions.length === 0}
        onChange={props.onExcludeChange}
      />
      {props.error ? <p className="field-error field-span-2">{t(props.error)}</p> : null}
    </>
  );
}

type ListProps = {
  id: string;
  heading: string;
  selectedLabel: string;
  emptyTitle: string;
  emptyHint: string;
  placeholder: string;
  selectedIDs: string[];
  blockedIDs: string[];
  subscriptions: Array<{ id: string; name: string; enabled: boolean }>;
  names: Map<string, { id: string; name: string; enabled: boolean }>;
  loading: boolean;
  onChange: (ids: string[]) => void;
};

function SubscriptionList(props: ListProps) {
  const { t } = useI18n();
  const blocked = new Set(props.blockedIDs);
  const options = props.subscriptions.map((item) => ({
    value: item.id,
    label: item.enabled ? item.name : `${item.name} (${t("已停用")})`,
    disabled: blocked.has(item.id),
  }));

  return (
    <div className="field-group field-span-2">
      <span className="field-label">{props.heading}</span>
      <Listbox
        id={props.id}
        multiple
        value={props.selectedIDs}
        onChange={props.onChange}
        aria-label={props.heading}
        disabled={props.loading && options.length === 0}
        placeholder={props.loading ? t("正在加载订阅...") : options.length ? props.placeholder : t("没有可添加的订阅")}
        options={options}
      />
      <span className="field-label">{props.selectedLabel}</span>
      {props.selectedIDs.length === 0 ? (
        <div className="fallback-empty">
          <strong>{props.emptyTitle}</strong>
          <p>{props.emptyHint}</p>
        </div>
      ) : (
        <ul className="picker-chips">
          {props.selectedIDs.map((id) => {
            const item = props.names.get(id);
            return (
              <li key={id} className="picker-chip">
                <span>{item ? item.name : t("已删除的订阅")}</span>
                {item && !item.enabled ? <span className="muted">{t("已停用")}</span> : null}
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => props.onChange(props.selectedIDs.filter((value) => value !== id))}
                  title={t("移除")}
                  aria-label={t("移除")}
                >
                  <Trash2 size={14} />
                </Button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
