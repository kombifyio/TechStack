import { fetchApi } from "./client";
import type { DiscoveredDevice, ScanResult } from "#lib/discovery/types.js";

export interface SubstrateConnection {
  server_id: string;
  name: string;
  worker_id: string;
  node: string;
  revision: number;
  available: boolean;
  connected?: boolean;
  enabled?: boolean;
}
export interface SubstrateProfile {
  id: string;
  title: string;
  architecture: string;
  min_cpu: number;
  min_memory_mib: number;
  min_disk_gib: number;
  appliance: boolean;
  cloud_init: boolean;
}
export interface SubstrateInventory {
  version: string;
  cpu: number;
  memory_available: number;
  storage: Array<{
    storage: string;
    content: string;
    active: number;
    avail: number;
  }>;
  networks: Array<{ iface: string; type: string; active: number }>;
}
export interface DiscoveryCapabilities {
  lan_executor_available: boolean;
  proxmox_fingerprinting: boolean;
  manual_connection_available: boolean;
}

async function data<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await fetchApi<T>(path, options);
  if (!response.data)
    throw new Error("The runtime did not return the requested data.");
  return response.data;
}

export const listSubstrates = () =>
  data<{ substrates: SubstrateConnection[] }>("/api/v1/substrates");
export const substrateProfiles = () =>
  data<{ profiles: SubstrateProfile[] }>("/api/v1/substrates/profiles");
export const enableSubstrate = (id: string, revision: number) =>
  data<{ revision: number; enabled: boolean }>(
    `/api/v1/substrates/${encodeURIComponent(id)}/binding`,
    {
      method: "PUT",
      body: JSON.stringify({ enabled: true, revision }),
    },
  );
export const substrateInventory = (id: string) =>
  data<SubstrateInventory>(
    `/api/v1/substrates/${encodeURIComponent(id)}/inventory`,
  );
export const discoveryCapabilities = () =>
  data<DiscoveryCapabilities>("/api/v1/discovery/capabilities");
export const discoveredSubstrateDevices = () =>
  data<{ devices: DiscoveredDevice[] }>("/api/v1/discovery/devices");
export const startSubstrateScan = () =>
  data<ScanResult>("/api/v1/discovery/scan", {
    method: "POST",
    body: JSON.stringify({ profile: "active", timeout_seconds: 30 }),
  });
export const substrateScanStatus = (id: string) =>
  data<ScanResult>(`/api/v1/discovery/scan/${encodeURIComponent(id)}`);

export function verifiedProxmoxDevices(
  devices: DiscoveredDevice[],
): DiscoveredDevice[] {
  return devices.filter((device) =>
    device.services?.some(
      (service) =>
        service.name === "proxmox" && service.fingerprint_verified === true,
    ),
  );
}
