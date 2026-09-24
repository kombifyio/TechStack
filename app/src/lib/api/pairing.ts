import { fetchApi } from "./client";

type PairingProvisioningMode = "install-command" | "connect-remote";

function pairingProvisioningMode(
  value: unknown,
): PairingProvisioningMode | null {
  if (value === "install-command" || value === "connect-remote") {
    return value;
  }
  return null;
}

function pairingRemoteFields(
  metadata: Record<string, unknown>,
): Record<string, unknown> {
  const remote: Record<string, unknown> = {};
  if (typeof metadata.server_remote_host === "string" && metadata.server_remote_host) {
    remote.server_remote_host = metadata.server_remote_host;
  }
  if (
    typeof metadata.server_remote_port === "number" &&
    metadata.server_remote_port > 0
  ) {
    remote.server_remote_port = metadata.server_remote_port;
  }
  if (typeof metadata.server_remote_user === "string" && metadata.server_remote_user) {
    remote.server_remote_user = metadata.server_remote_user;
  }
  if (
    typeof metadata.server_remote_auth_method === "string" &&
    metadata.server_remote_auth_method
  ) {
    remote.server_remote_auth_method = metadata.server_remote_auth_method;
  }
  const sshKeyLabel =
    typeof metadata.server_remote_credential_ref === "string"
      ? metadata.server_remote_credential_ref
      : typeof metadata.server_remote_ssh_key_label === "string"
        ? metadata.server_remote_ssh_key_label
        : "";
  if (sshKeyLabel) {
    remote.server_remote_ssh_key_label = sshKeyLabel;
  }
  if (metadata.server_remote_use_sudo === true) {
    remote.server_remote_use_sudo = true;
  }
  return remote;
}

/** Mint only on an explicit operator action; job projections remain secret-free. */
export async function renewNodePairing(
  deploymentId: string,
  metadata: Record<string, unknown>,
): Promise<{ token: string; expires_at: string }> {
  const mode = pairingProvisioningMode(metadata.server_provisioning_mode);
  const role = metadata.server_node_role;
  const foundation = metadata.stackkit_foundation;
  const services = metadata.requested_services;
  if (
    !deploymentId ||
    !mode ||
    typeof role !== "string" ||
    !role ||
    typeof foundation !== "string" ||
    !foundation ||
    !(services === null || Array.isArray(services)) ||
    (Array.isArray(services) &&
      services.some((item) => typeof item !== "string"))
  ) {
    throw new Error(
      "The original Node configuration is unavailable. Return to Add Node to prepare the connection.",
    );
  }
  const response = await fetchApi<{ token: string; expires_at: string }>(
    "/api/v1/trust/pairing-tokens",
    {
      method: "POST",
      body: JSON.stringify({
        // Existing API compatibility boundary; callers use deploymentId.
        stack_id: deploymentId,
        name: "Node connection",
        server_provisioning_mode: mode,
        node_role: role,
        stackkit: foundation,
        services: services ?? [],
        ...(typeof metadata.environment_class === "string"
          ? { environment_class: metadata.environment_class }
          : {}),
        ...(mode === "connect-remote" ? pairingRemoteFields(metadata) : {}),
      }),
    },
  );
  if (!response.data?.token || !response.data.expires_at) {
    throw new Error("The connection command could not be prepared. Try again.");
  }
  return response.data;
}
