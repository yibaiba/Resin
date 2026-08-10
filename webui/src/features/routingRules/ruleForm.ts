import type { RoutingPortRange, RoutingRule, RoutingRuleInput } from "./types";

export type RoutingRuleForm = {
  name: string;
  enabled: boolean;
  hostType: RoutingRuleInput["host_type"];
  hostValue: string;
  ports: string;
  network: RoutingRuleInput["network"];
  action: RoutingRuleInput["action"];
  platformID: string;
  overrideExplicitPlatform: boolean;
};

export const emptyRoutingRuleForm: RoutingRuleForm = {
  name: "",
  enabled: true,
  hostType: "ANY",
  hostValue: "",
  ports: "",
  network: "ANY",
  action: "PLATFORM",
  platformID: "",
  overrideExplicitPlatform: false,
};

export function routingRuleToForm(rule: RoutingRule): RoutingRuleForm {
  return {
    name: rule.name,
    enabled: rule.enabled,
    hostType: rule.host_type,
    hostValue: rule.host_value,
    ports: formatPortRanges(rule.ports),
    network: rule.network,
    action: rule.action,
    platformID: rule.platform_id ?? "",
    overrideExplicitPlatform: rule.override_explicit_platform,
  };
}

export function routingRuleFormToInput(form: RoutingRuleForm): RoutingRuleInput {
  const name = form.name.trim();
  if (!name) {
    throw new Error("规则名称不能为空");
  }
  const hostValue = form.hostValue.trim();
  if (form.hostType !== "ANY" && !hostValue) {
    throw new Error("当前匹配类型需要主机值");
  }
  if (form.action === "PLATFORM" && !form.platformID) {
    throw new Error("Platform 动作必须选择平台");
  }
  return {
    name,
    enabled: form.enabled,
    host_type: form.hostType,
    host_value: form.hostType === "ANY" ? "" : hostValue,
    ports: parsePortRanges(form.ports),
    network: form.network,
    action: form.action,
    platform_id: form.action === "PLATFORM" ? form.platformID : "",
    override_explicit_platform: form.overrideExplicitPlatform,
  };
}

function parsePortRanges(raw: string): RoutingPortRange[] {
  if (!raw.trim()) {
    return [];
  }
  return raw.split(",").map((part) => {
    const values = part.trim().split("-").map(Number);
    const start = values[0];
    const end = values.length === 1 ? start : values[1];
    if (!Number.isInteger(start) || !Number.isInteger(end) || start < 1 || end > 65535 || start > end) {
      throw new Error(`端口范围无效：${part.trim()}`);
    }
    return { start, end };
  });
}

export function formatPortRanges(ranges: RoutingPortRange[]): string {
  return ranges.map((range) => (range.start === range.end ? String(range.start) : `${range.start}-${range.end}`)).join(", ");
}
