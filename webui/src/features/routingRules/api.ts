import { apiRequest } from "../../lib/api-client";
import type { PageResponse } from "../platforms/types";
import type { RoutingDecision, RoutingRule, RoutingRuleInput } from "./types";

const basePath = "/api/v1/routing-rules";

export async function listRoutingRules(): Promise<RoutingRule[]> {
  const page = await apiRequest<PageResponse<RoutingRule>>(`${basePath}?limit=1000&offset=0`);
  return [...page.items].sort((left, right) => left.priority - right.priority);
}

export function createRoutingRule(input: RoutingRuleInput): Promise<RoutingRule> {
  return apiRequest<RoutingRule>(basePath, { method: "POST", body: input });
}

export function updateRoutingRule(id: string, input: Partial<RoutingRuleInput>): Promise<RoutingRule> {
  return apiRequest<RoutingRule>(`${basePath}/${id}`, { method: "PATCH", body: input });
}

export async function deleteRoutingRule(id: string): Promise<void> {
  await apiRequest<void>(`${basePath}/${id}`, { method: "DELETE" });
}

export async function reorderRoutingRules(ruleIDs: string[]): Promise<void> {
  await apiRequest<void>(`${basePath}/order`, { method: "PUT", body: { rule_ids: ruleIDs } });
}

export function previewRoutingDecision(input: {
  platform_name: string;
  target: string;
  network: string;
}): Promise<RoutingDecision> {
  return apiRequest<RoutingDecision>(`${basePath}/preview-match`, { method: "POST", body: input });
}
