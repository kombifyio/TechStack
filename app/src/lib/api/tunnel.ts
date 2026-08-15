/**
 * Tunnel / Registry URL API
 *
 * Provides the "worker registry" URL that workers should use to reach the Core.
 */

import { fetchApi } from "./client";

export type TunnelMode = "auto" | "local" | "tunnel" | "tailscale" | "custom";

export interface RegistryUrlResult {
  url: string;
  mode: TunnelMode | string;
}

export interface LocalLANBridgeStatus {
  enabled: boolean;
  origin?: string;
  address?: string;
  port: number;
  mode: string;
  error?: string;
}

/**
 * Fetch the best public URL for worker registration.
 *
 * Backend endpoint: GET /api/v1/tunnel/registry-url
 */
export async function getWorkerRegistryUrl(): Promise<RegistryUrlResult> {
  const res = await fetchApi<RegistryUrlResult>("/api/v1/tunnel/registry-url");
  return res.data;
}

export async function setLocalLANBridge(
  enabled: boolean,
): Promise<LocalLANBridgeStatus> {
  const res = await fetchApi<LocalLANBridgeStatus>(
    "/api/v1/client/lan-bridge",
    { method: "PUT", body: JSON.stringify({ enabled }) },
  );
  return res.data;
}
