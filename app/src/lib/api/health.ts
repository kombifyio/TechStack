/**
 * kombify-TechStack API - Health & Info Module
 *
 * Health status and API information endpoints.
 */

import { get } from "./client";
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
  version: string;
  revision?: string;
  edition?: DeploymentEdition;
  deployment_mode?: DeploymentMode;
  service: string;
  go_version?: string;
  goroutines?: number;
  memory_mb?: number;
  database_backend?: string;
  environment?: string;
  uptime_secs?: number;
}

// ============================================================================
// API Functions
// ============================================================================

/**
 * Get health status of the API
 */
export async function getHealth(): Promise<HealthStatus> {
  return get<HealthStatus>("/api/v1/health");
}

/**
 * Get API information and runtime stats
 */
export async function getInfo(): Promise<ApiInfo> {
  const info = await get<ApiInfo>("/api/v1/info");
  if (!info || typeof info.version !== "string" || !info.version.trim()) {
    throw new Error("Techstack info response is missing product version");
  }
  return info;
}
