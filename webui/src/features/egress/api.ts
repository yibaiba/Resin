import { apiRequest } from "../../lib/api-client";
import type { PageResponse } from "../platforms/types";
import type { BatchDrainResponse, EgressIP, EgressPolicyInput, EgressRotationResponse, RotateEgressInput } from "./types";

const basePath = "/api/v1/egress-ips";

export async function listEgressIPs(
  keyword: string,
  limit: number,
  offset: number,
): Promise<PageResponse<EgressIP>> {
  const query = new URLSearchParams({ limit: String(limit), offset: String(offset) });
  if (keyword.trim()) query.set("keyword", keyword.trim());
  return apiRequest<PageResponse<EgressIP>>(basePath + "?" + query.toString());
}

export function updateEgressPolicy(ip: string, input: EgressPolicyInput): Promise<EgressIP> {
  return apiRequest<EgressIP>(`${basePath}/${encodeURIComponent(ip)}`, { method: "PATCH", body: input });
}

export function batchDrainEgressIPs(egressIPs: string[], resumeAt: string): Promise<BatchDrainResponse> {
  return apiRequest<BatchDrainResponse>(`${basePath}/actions/batch-drain`, { method: "POST", body: { egress_ips: egressIPs, resume_at: resumeAt } });
}

export function rotateEgressLeases(ip: string, input: RotateEgressInput): Promise<EgressRotationResponse> {
  return apiRequest<EgressRotationResponse>(`${basePath}/${encodeURIComponent(ip)}/actions/rotate-leases`, { method: "POST", body: input });
}
