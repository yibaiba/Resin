import { useQuery } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { Button } from "../../components/ui/Button";
import { Input } from "../../components/ui/Input";
import { Listbox } from "../../components/ui/Listbox";
import { useI18n } from "../../i18n";
import { listAllSubscriptions } from "../subscriptions/api";

type Props = {
  selectedIDs: string[];
  create?: boolean;
  onChange: (ids: string[]) => void;
};

export function PlatformSubscriptionPicker(props: Props) {
  const { t } = useI18n();
  const [candidate, setCandidate] = useState("");
  const [keyword, setKeyword] = useState("");
  const fieldID = props.create ? "create-subscription-filter" : "detail-edit-subscription-filter";
  const searchID = `${fieldID}-search`;

  const subscriptionsQuery = useQuery({
    queryKey: ["subscriptions", "platform-picker"],
    queryFn: () => listAllSubscriptions(),
  });
  const subscriptions = subscriptionsQuery.data ?? [];
  const names = useMemo(() => new Map(subscriptions.map((item) => [item.id, item])), [subscriptions]);
  const needle = keyword.trim().toLowerCase();
  const available = subscriptions
    .filter((item) => !props.selectedIDs.includes(item.id))
    .filter((item) => !needle || item.name.toLowerCase().includes(needle) || item.id.toLowerCase().includes(needle))
    .slice()
    .sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" }));
  const add = () => {
    if (!candidate) return;
    props.onChange([...props.selectedIDs, candidate]);
    setCandidate("");
  };

  return (
    <>
      <div className="routing-settings-heading field-span-2">
        <h5>{t("允许的订阅")}</h5>
        <p>
          {t("不选择订阅时，所有订阅的节点都可以进入该平台。添加后仅使用这些订阅里的节点，可再叠加节点名正则和地区过滤。")}
        </p>
      </div>
      <div className="field-group">
        <label className="field-label" htmlFor={searchID}>
          {t("搜索订阅")}
        </label>
        <Input
          id={searchID}
          value={keyword}
          placeholder={t("按名称筛选订阅")}
          onChange={(event) => setKeyword(event.target.value)}
        />
      </div>
      <div className="field-group">
        <label className="field-label" htmlFor={fieldID}>
          {t("添加订阅")}
        </label>
        <div className="fallback-add-row">
          <Listbox
            id={fieldID}
            value={candidate}
            onChange={setCandidate}
            aria-label={t("选择订阅")}
            disabled={subscriptionsQuery.isLoading || available.length === 0}
            placeholder={
              subscriptionsQuery.isLoading
                ? t("正在加载订阅...")
                : available.length
                  ? t("选择订阅")
                  : t("没有可添加的订阅")
            }
            options={available.map((item) => ({
              value: item.id,
              label: item.enabled ? item.name : `${item.name} (${t("已停用")})`,
            }))}
          />
          <Button variant="secondary" size="sm" onClick={add} disabled={!candidate}>
            <Plus size={15} />
            {t("添加")}
          </Button>
        </div>
      </div>
      <div className="field-group fallback-order-field">
        <span className="field-label">{t("已选订阅")}</span>
        {props.selectedIDs.length === 0 ? (
          <div className="fallback-empty">
            <strong>{t("未限制订阅")}</strong>
            <p>{t("当前平台会接收所有订阅中符合其他过滤规则的节点。")}</p>
          </div>
        ) : null}
        <ol className="fallback-list">
          {props.selectedIDs.map((id) => {
            const item = names.get(id);
            return (
              <li key={id}>
                <span className="fallback-name">{item ? item.name : t("已删除的订阅")}</span>
                {!item ? <span className="muted">{id}</span> : null}
                {item && !item.enabled ? <span className="muted">{t("已停用")}</span> : null}
                <div className="fallback-actions">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => props.onChange(props.selectedIDs.filter((value) => value !== id))}
                    title={t("移除")}
                    aria-label={t("移除")}
                  >
                    <Trash2 size={14} />
                  </Button>
                </div>
              </li>
            );
          })}
        </ol>
      </div>
    </>
  );
}
