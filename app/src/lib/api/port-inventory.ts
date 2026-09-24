import { fetchApi } from "./client";

export type PortEvidenceState = "unknown" | "present" | "missing" | "stale";
export type PortDriftState =
  "consistent" | "missing" | "unexpected" | "unknown";

export interface PortAllocation {
  id: string;
  kit_deployment_id?: string;
  resolved_plan_hash?: string;
  node_ref?: string;
  transport: "tcp" | "udp";
  bind_address: string;
  port: number;
  sharing?: "exclusive" | "virtual-host";
  listener_group_ref?: string;
  exposure: "local" | "remote-private" | "public";
  source_route_refs?: string[];
  reservation_state?: "reserved" | "released";
  claim_state?: "pending" | "mutating" | "active" | "uncertain" | "released";
  observed_state: PortEvidenceState;
  exposed_state: PortEvidenceState;
  drift_state: PortDriftState;
  desired: boolean;
}

export interface ServerPortInventory {
  server_id: string;
  server_generation: number;
  observed_at?: string;
  expires_at?: string;
  inventory_revision: number;
  listeners_complete: boolean;
  exposures_complete: boolean;
  allocations: PortAllocation[];
}

export async function getServerPortInventory(
  serverId: string,
): Promise<ServerPortInventory> {
  const response = await fetchApi<ServerPortInventory>(
    `/api/v1/servers/${encodeURIComponent(serverId)}/ports`,
  );
  return response.data;
}
