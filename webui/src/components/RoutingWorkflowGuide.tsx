import { FlaskConical, GitFork, Globe2, RefreshCw, Route } from "lucide-react";
import { Link } from "react-router-dom";
import { useI18n } from "../i18n";
import { cn } from "../lib/cn";

type RoutingWorkflowStep = "rules" | "fallback" | "egress" | "rotation" | "diagnostics";

type Props = {
  active: RoutingWorkflowStep | RoutingWorkflowStep[];
  embedded?: boolean;
};

const steps = [
  { id: "rules", label: "目标规则", description: "决定请求走哪里", path: "/routing-rules", icon: Route },
  { id: "fallback", label: "平台回退", description: "主平台无节点时接力", path: "/platforms", icon: GitFork },
  { id: "egress", label: "出口策略", description: "控制 IP 状态与容量", path: "/egress-ips", icon: Globe2 },
  { id: "rotation", label: "粘性轮换", description: "决定何时更换出口", path: "/platforms", icon: RefreshCw },
  { id: "diagnostics", label: "路由诊断", description: "解释最终选择结果", path: "/routing-diagnostics", icon: FlaskConical },
] as const;

export function RoutingWorkflowGuide({ active, embedded = false }: Props) {
  const { t } = useI18n();
  const activeSteps = Array.isArray(active) ? active : [active];
  return (
    <nav className={cn(!embedded && "card", "routing-workflow", embedded && "is-embedded")} aria-label={t("路由控制流程")}>
      <div className="routing-workflow-heading">
        <strong>{t("路由控制流程")}</strong>
        <span>{t("按流量经过系统的顺序配置；日常排障可直接从诊断开始。")}</span>
      </div>
      <ol>
        {steps.map((step, index) => {
          const Icon = step.icon;
          return (
            <li key={step.id}>
              <Link className={cn("routing-workflow-step", activeSteps.includes(step.id) && "is-active")} to={step.path}>
                <span className="routing-workflow-index">{index + 1}</span>
                <Icon size={16} aria-hidden="true" />
                <span><strong>{t(step.label)}</strong><small>{t(step.description)}</small></span>
              </Link>
            </li>
          );
        })}
      </ol>
    </nav>
  );
}
