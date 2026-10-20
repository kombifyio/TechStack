import { fetchApi } from "./client";

/** The class the Layout Planner reads for a machine. */
export type NodeAvailabilityClass = "always-on" | "part-time" | "unknown";

/** What a confirmation can set: `unknown` withdraws it. */
export type NodeAvailabilityChoice = NodeAvailabilityClass;

/**
 * How available one server is. `class` is the owner's confirmation when there
 * is one and the observed class otherwise; `source` says which.
 */
export interface ServerAvailability {
  server_id: string;
  class: NodeAvailabilityClass;
  source: "user-confirmed" | "server-transitions" | "none";
  /** What the connection history alone supports. */
  observed_class: NodeAvailabilityClass;
  /** Share of the observed time the server was connected; null without history. */
  uptime_ratio: number | null;
  observed_seconds: number;
  window_days: number;
  confirmed: boolean;
  confirmed_at?: string;
}

export async function getServerAvailability(
  serverId: string,
): Promise<ServerAvailability> {
  const response = await fetchApi<ServerAvailability>(
    `/api/v1/servers/${encodeURIComponent(serverId)}/availability`,
  );
  return response.data;
}

/**
 * Confirm or correct how available a server is. `unknown` withdraws the
 * confirmation and goes back to what the connection history supports.
 */
export async function confirmServerAvailability(
  serverId: string,
  choice: NodeAvailabilityChoice,
): Promise<ServerAvailability> {
  const response = await fetchApi<ServerAvailability>(
    `/api/v1/servers/${encodeURIComponent(serverId)}/availability`,
    { method: "PUT", body: JSON.stringify({ class: choice }) },
  );
  return response.data;
}
