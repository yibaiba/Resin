import type {
  PlatformAllocationPolicy,
  PlatformEmptyAccountBehavior,
  PlatformMissAction,
  RotationStrategy,
} from "./types";

export const allocationPolicies: PlatformAllocationPolicy[] = [
  "BALANCED",
  "PREFER_LOW_LATENCY",
  "PREFER_IDLE_IP",
];

export const rotationStrategies: RotationStrategy[] = [
  "ANY",
  "SAME_IP",
  "PREFER_NEW_IP",
  "REQUIRE_NEW_IP",
];

export const rotationStrategyLabel: Record<RotationStrategy, string> = {
  ANY: "任意可用出口",
  SAME_IP: "保持当前出口 IP",
  PREFER_NEW_IP: "优先切换出口 IP",
  REQUIRE_NEW_IP: "必须切换出口 IP",
};

export const rotationStrategyDescription: Record<RotationStrategy, string> = {
  ANY: "更换为任意其他可用节点，不要求出口 IP 发生变化。",
  SAME_IP: "只在当前出口 IP 内更换节点；没有替代节点时延后轮换。",
  PREFER_NEW_IP: "优先切换到新出口 IP，不可用时允许更换为其他节点。",
  REQUIRE_NEW_IP: "只有找到新出口 IP 才轮换，否则保留原租约并标记延期。",
};

export const missActions: PlatformMissAction[] = ["TREAT_AS_EMPTY", "REJECT"];

export const emptyAccountBehaviors: PlatformEmptyAccountBehavior[] = [
  "RANDOM",
  "FIXED_HEADER",
  "ACCOUNT_HEADER_RULE",
];

export const allocationPolicyLabel: Record<PlatformAllocationPolicy, string> = {
  BALANCED: "均衡",
  PREFER_LOW_LATENCY: "优先低延迟",
  PREFER_IDLE_IP: "优先空闲出口 IP",
};

export const missActionLabel: Record<PlatformMissAction, string> = {
  TREAT_AS_EMPTY: "按空账号处理",
  REJECT: "拒绝代理请求",
};

export const emptyAccountBehaviorLabel: Record<PlatformEmptyAccountBehavior, string> = {
  RANDOM: "随机路由",
  FIXED_HEADER: "提取指定请求头作为 Account",
  ACCOUNT_HEADER_RULE: "按照全局请求头规则提取 Account",
};
