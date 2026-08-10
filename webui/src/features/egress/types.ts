import type { RotationStrategy } from "../platforms/types";

export type EgressStatus = "ACTIVE" | "DRAINING" | "BLOCKED";

export type EgressIP = {
  egress_ip: string;
  status: EgressStatus;
  configured_status: EgressStatus;
  node_count: number;
  platform_ids: string[];
  active_lease_count: number;
  max_active_leases: number;
  resume_at?: string;
  updated_at?: string;
};

export type EgressPolicyInput = {
  status: EgressStatus;
  max_active_leases: number;
  resume_at: string;
};

export type BatchEgressResult = { egress_ip: string; success: boolean; error?: string };
export type EgressRotationResult = { platform_id: string; account: string; status: string; error?: string };
export type BatchDrainResponse = { items: BatchEgressResult[] };
export type EgressRotationResponse = { items: EgressRotationResult[] };
export type RotateEgressInput = { strategy: RotationStrategy };
