export type DiagnosticMode = "DRY_RUN" | "LIVE_TCP";

export type DiagnosticRequest = {
  mode: DiagnosticMode;
  platform_name: string;
  account: string;
  target: string;
  required_network: "tcp" | "udp";
  seed?: number;
  timeout: string;
};

export type DecisionRuleTrace = { id: string; name: string; matched: boolean; reason: string };
export type DiagnosticPlatform = { id: string; name: string };
export type DiagnosticCandidate = {
  platform_id: string;
  node_hash: string;
  egress_ip: string;
  status: string;
  active_lease_count: number;
  max_active_leases: number;
  eligible: boolean;
  exclusions: string[];
};
export type P2CTrace = {
  first_node_hash?: string;
  second_node_hash?: string;
  first_score: number;
  second_score: number;
  selected_hash?: string;
};
export type DiagnosticP2C = { platform_id: string; trace: P2CTrace };
export type DiagnosticSelection = {
  platform_id: string;
  platform_name: string;
  node_hash: string;
  egress_ip: string;
  reason: string;
  rotation_status?: string;
  rotation_reason?: string;
  rotation_strategy?: string;
};
export type DiagnosticLease = {
  selected_platform_id: string;
  account: string;
  node_hash: string;
  egress_ip: string;
  rotation_deferred: boolean;
  rotation_deferred_reason: string;
};

export type DiagnosticResponse = {
  mode: DiagnosticMode;
  decision: {
    action: "ROUTED" | "DIRECT" | "REJECT";
    platform_id?: string;
    platform_name?: string;
    matched_rule_id?: string;
    matched_rule_name?: string;
    reason: string;
    explicit_platform_kept: boolean;
    target: { host: string; port: number };
  };
  decision_trace: {
    environment_bypass_matched: boolean;
    explicit_platform: boolean;
    rules: DecisionRuleTrace[];
  };
  route?: {
    seed: number;
    root: DiagnosticPlatform;
    fallback: DiagnosticPlatform[];
    lease?: DiagnosticLease;
    candidates: DiagnosticCandidate[];
    p2c: DiagnosticP2C[];
    selection?: DiagnosticSelection;
    outcome: string;
  };
  live_tcp?: { attempted: boolean; success: boolean; duration_ms: number; error?: string; reason?: string };
};
