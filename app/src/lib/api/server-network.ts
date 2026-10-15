import { fetchApi } from "./client";

export type NetworkRebindStatus = "queued" | "running" | "succeeded" | "failed";

/** One network position of a server; every field is optional. */
export interface NetworkPoint {
  ip?: string;
  gateway?: string;
  cidr?: string;
  ssid?: string;
  interface?: string;
  wireless?: boolean;
  fingerprint?: string;
}

export interface NetworkMove {
  detected_at: string;
  previous: NetworkPoint;
  current: NetworkPoint;
  rebind_status: NetworkRebindStatus;
}

export interface NetworkRebind {
  status?: NetworkRebindStatus;
  trigger?: "auto" | "manual";
  job_id?: string;
  requested_at?: string;
  reason?: string;
}

export interface ServerNetwork {
  server_id: string;
  current: NetworkPoint;
  network_move?: NetworkMove;
  rebind?: NetworkRebind;
  can_rebind: boolean;
  connection_state: string;
  observed_at?: string;
  /** Message ids of owner guidance, for example `offline_move_hint`. */
  guidance?: string[];
}

export interface ServerNetworkRebindAccepted {
  server_id: string;
  job_id: string;
  status: "queued" | "running";
}

export async function getServerNetwork(
  serverId: string,
): Promise<ServerNetwork> {
  const response = await fetchApi<ServerNetwork>(
    `/api/v1/servers/${encodeURIComponent(serverId)}/network`,
  );
  return response.data;
}

/**
 * Re-bind a moved server to the network it is on now. The node re-detects its
 * address on its own, so the request carries no input.
 */
export async function rebindServerNetwork(
  serverId: string,
): Promise<ServerNetworkRebindAccepted> {
  const response = await fetchApi<ServerNetworkRebindAccepted>(
    `/api/v1/servers/${encodeURIComponent(serverId)}/network/rebind`,
    { method: "POST" },
  );
  return response.data;
}
