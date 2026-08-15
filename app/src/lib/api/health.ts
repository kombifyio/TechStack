/**
 * kombify-TechStack API - Health & Info Module
 *
 * Health status and API information endpoints.
 */

import { fetchApi, API_BASE } from "./client";
import type { DeploymentEdition, DeploymentMode } from "./auth";

// ============================================================================
// Types
// ============================================================================

export interface HealthStatus {
  status: string;
  version: string;
  revision?: string;
  edition?: DeploymentEdition;
  deployment_mode?: DeploymentMode;
  service: string;
  time: string;
}

export interface ApiInfo {
  name?: string;
  version: string;
  revision?: string;
  edition?: DeploymentEdition;
  deployment_mode?: DeploymentMode;
  description?: string;
  service: string;
  configured?: boolean;
  endpoints?: string[];
  go_version?: string;
  goroutines?: number;
  memory_mb?: number;
  pocketbase?: boolean;
  uptime_secs?: number;
}

type MaybeWrappedApiInfo = ApiInfo | { data?: ApiInfo };

function unwrapApiInfo(payload: MaybeWrappedApiInfo): ApiInfo {
  if (
    payload &&
    typeof payload === "object" &&
    "data" in payload &&
    payload.data &&
    typeof payload.data === "object"
  ) {
    return payload.data;
  }
  return payload as ApiInfo;
}

// ============================================================================
// API Functions
// ============================================================================

/**
 * Get health status of the API
 */
export async function getHealth(): Promise<HealthStatus> {
  const res = await fetchApi<HealthStatus>("/api/v1/health");
  return res.data;
}

/**
 * Get API information and runtime stats
 */
export async function getInfo(): Promise<ApiInfo> {
  const url = `${API_BASE}/api/v1/info`;
  const response = await fetch(url, {
    headers: { "Content-Type": "application/json" },
  });
  if (!response.ok) {
    throw new Error(
      `TechStack info request failed with HTTP ${response.status}`,
    );
  }
  const info = unwrapApiInfo((await response.json()) as MaybeWrappedApiInfo);
  if (!info || typeof info.version !== "string" || !info.version.trim()) {
    throw new Error("TechStack info response is missing product version");
  }
  return info;
}
