import { apiRequest } from "../../lib/api-client";
import type { DiagnosticRequest, DiagnosticResponse } from "./types";

export function diagnoseRoute(input: DiagnosticRequest): Promise<DiagnosticResponse> {
  return apiRequest<DiagnosticResponse>("/api/v1/routing-diagnostics", { method: "POST", body: input });
}
