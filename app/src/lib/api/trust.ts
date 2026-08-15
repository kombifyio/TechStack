import { fetchApi } from "./client";

export interface PairingToken {
  id?: string;
  token: string;
  expires_at: string;
  job_id?: string;
  stack_id?: string;
}

export interface PairingTokenOptions {
  stackId?: string;
  serverProvisioningMode?: "install-command" | "connect-remote";
  nodeRole?: string;
  stackkit?: string;
  services?: string[];
  remoteHost?: string;
  remotePort?: number | null;
  remoteUser?: string;
  remoteAuthMethod?: "ssh-key" | "password";
  remoteSSHKeyLabel?: string;
  remoteUseSudo?: boolean;
}

type TrustRequestInit = Omit<RequestInit, "credentials" | "headers"> & {
  headers?: Record<string, string>;
};

function trustURL(path: string): string {
  return `/api/v1/trust/${path.replace(/^\/+/, "")}`;
}

async function requestTrust<T>(
  path: string,
  init: TrustRequestInit = {},
): Promise<T> {
  const response = await fetchApi<T>(trustURL(path), init);
  return response.data;
}

function postJSON(input: unknown): TrustRequestInit {
  return {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  };
}

export async function createPairingToken(
  name: string,
  options: PairingTokenOptions = {},
): Promise<PairingToken> {
  return requestTrust<PairingToken>(
    "pairing-tokens",
    postJSON({
      name: name ? `Enroll: ${name}` : "Remote enroll",
      expiry_minutes: 15,
      stack_id: options.stackId,
      server_provisioning_mode: options.serverProvisioningMode,
      node_role: options.nodeRole,
      stackkit: options.stackkit,
      services: options.services,
      server_remote_host: options.remoteHost,
      server_remote_port: options.remotePort,
      server_remote_user: options.remoteUser,
      server_remote_auth_method: options.remoteAuthMethod,
      server_remote_ssh_key_label: options.remoteSSHKeyLabel,
      server_remote_use_sudo: options.remoteUseSudo,
    }),
  );
}
