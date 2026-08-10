export type PlatformMissAction = "TREAT_AS_EMPTY" | "REJECT";
export type PlatformEmptyAccountBehavior = "RANDOM" | "FIXED_HEADER" | "ACCOUNT_HEADER_RULE";
export type PlatformAllocationPolicy = "BALANCED" | "PREFER_LOW_LATENCY" | "PREFER_IDLE_IP";
export type RotationStrategy = "ANY" | "SAME_IP" | "PREFER_NEW_IP" | "REQUIRE_NEW_IP";

export type Platform = {
  id: string;
  name: string;
  sticky_ttl: string;
  regex_filters: string[];
  region_filters: string[];
  routable_node_count: number;
  reverse_proxy_miss_action: PlatformMissAction;
  reverse_proxy_empty_account_behavior: PlatformEmptyAccountBehavior;
  reverse_proxy_fixed_account_header: string;
  allocation_policy: PlatformAllocationPolicy;
  passive_circuit_breaker_disabled: boolean;
  fallback_platform_ids: string[];
  rotation_interval: string;
  rotation_strategy: RotationStrategy;
  updated_at: string;
};

export type PageResponse<T> = {
  items: T[];
  total: number;
  limit: number;
  offset: number;
};

export type PlatformCreateInput = {
  name: string;
  sticky_ttl?: string;
  regex_filters?: string[];
  region_filters?: string[];
  reverse_proxy_miss_action?: PlatformMissAction;
  reverse_proxy_empty_account_behavior?: PlatformEmptyAccountBehavior;
  reverse_proxy_fixed_account_header?: string;
  allocation_policy?: PlatformAllocationPolicy;
  passive_circuit_breaker_disabled?: boolean;
  fallback_platform_ids?: string[];
  rotation_interval?: string;
  rotation_strategy?: RotationStrategy;
};

export type PlatformUpdateInput = {
  name?: string;
  sticky_ttl?: string;
  regex_filters?: string[];
  region_filters?: string[];
  reverse_proxy_miss_action?: PlatformMissAction;
  reverse_proxy_empty_account_behavior?: PlatformEmptyAccountBehavior;
  reverse_proxy_fixed_account_header?: string;
  allocation_policy?: PlatformAllocationPolicy;
  passive_circuit_breaker_disabled?: boolean;
  fallback_platform_ids?: string[];
  rotation_interval?: string;
  rotation_strategy?: RotationStrategy;
};

export type PlatformLease = {
  platform_id: string;
  selected_platform_id: string;
  account: string;
  node_hash: string;
  node_tag: string;
  egress_ip: string;
  expiry: string;
  last_accessed: string;
  last_rotated_at: string;
  rotation_requested_at: string;
  rotation_requested_strategy: string;
  rotation_deferred: boolean;
  rotation_deferred_reason: string;
};

export type RotateLeaseResult = {
  status: string;
  reason: string;
  strategy: RotationStrategy;
  lease?: PlatformLease;
};

export type PlatformLeaseSortBy = "account" | "expiry" | "last_accessed";
export type SortOrder = "asc" | "desc";

export type ListPlatformLeasesInput = {
  limit?: number;
  offset?: number;
  account?: string;
  fuzzy?: boolean;
  sort_by?: PlatformLeaseSortBy;
  sort_order?: SortOrder;
};
