export type RoutingHostType = "ANY" | "EXACT" | "DOMAIN_SUFFIX" | "WILDCARD" | "CIDR";
export type RoutingNetwork = "ANY" | "TCP" | "UDP";
export type RoutingAction = "PLATFORM" | "DIRECT" | "REJECT";

export type RoutingPortRange = {
  start: number;
  end: number;
};

export type RoutingRule = {
  id: string;
  name: string;
  enabled: boolean;
  priority: number;
  host_type: RoutingHostType;
  host_value: string;
  ports: RoutingPortRange[];
  network: RoutingNetwork;
  action: RoutingAction;
  platform_id?: string;
  override_explicit_platform: boolean;
  created_at: string;
  updated_at: string;
};

export type RoutingRuleInput = Omit<RoutingRule, "id" | "priority" | "created_at" | "updated_at">;

export type RoutingDecision = {
  action: "ROUTED" | "DIRECT" | "REJECT";
  platform_id?: string;
  platform_name?: string;
  matched_rule_id?: string;
  matched_rule_name?: string;
  reason: string;
  explicit_platform_kept: boolean;
  target: { host: string; port: number };
};
