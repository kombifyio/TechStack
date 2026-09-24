import { fetchApi } from "./client";

export interface ServerAccess {
  server_id: string;
  host?: string;
  user?: string;
  port?: number;
  host_key_fingerprint?: string;
  user_key_fingerprint?: string;
  ssh_command?: string;
  terminal_enabled: boolean;
  manual_command_available: boolean;
  connect_ready: boolean;
  reason?: string;
}

export interface TerminalSession {
  session_id: string;
  stream_url: string;
  expires_at: string;
  idle_limit_seconds: number;
}

export async function getServerAccess(serverId: string): Promise<ServerAccess> {
  const response = await fetchApi<ServerAccess>(
    `/api/v1/servers/${encodeURIComponent(serverId)}/access`,
  );
  return response.data;
}

export async function createTerminalSession(
  serverId: string,
): Promise<TerminalSession> {
  const response = await fetchApi<TerminalSession>(
    `/api/v1/servers/${encodeURIComponent(serverId)}/terminal-sessions`,
    { method: "POST", credentials: "include" },
  );
  return response.data;
}

export async function authorizeServerSSHKey(
  serverId: string,
  walletItemId: string,
): Promise<ServerAccess> {
  const response = await fetchApi<ServerAccess>(
    `/api/v1/servers/${encodeURIComponent(serverId)}/access/authorized-key`,
    {
      method: "POST",
      credentials: "include",
      body: JSON.stringify({ wallet_item_id: walletItemId, confirm: true }),
    },
  );
  return response.data;
}
