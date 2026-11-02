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
  /** Key pin of the private-LAN bridge, present only when url is that bridge. */
  lan_pin?: string;
}

export interface LocalLANBridgeStatus {
  enabled: boolean;
  origin?: string;
  pin?: string;
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

export interface GuardReinstallCommand {
  linux: string;
  expires_at?: string;
}

/**
 * Mint the command that reinstalls the Guard of an enrolled server in place.
 * Its capability only downloads the Guard; the enrollment stays as it is.
 *
 * Backend endpoint: POST /api/v1/tunnel/install-command (mode reinstall-guard)
 */
export async function createGuardReinstallCommand(): Promise<GuardReinstallCommand> {
  const res = await fetchApi<GuardReinstallCommand>(
    "/api/v1/tunnel/install-command",
    { method: "POST", body: JSON.stringify({ mode: "reinstall-guard" }) },
  );
  return res.data;
}
