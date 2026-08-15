/**
 * kombify-TechStack Wizard Payload Builder
 *
 * Transforms StackConfig into API-compatible CreateStackRequest
 */

import {
  createDefaultIdentityConfig,
  isManagedServerMode,
  normalizeServerProvisioningConfig,
  type IdentityConfig,
  type OwnerBootstrapMode,
  type OwnerSource,
  type ServicesConfig,
  type StackConfig,
} from "./types";
import {
  applyStandardBundleGoalServices,
  buildPayloadServicesFromBundle,
  selectedCanonicalUseCasesFromGoals,
} from "./standardBundle";

/**
 * API payload structure (matches backend expectations)
 */
export interface CreateStackPayload {
  name: string;
  provider: string;
  services: string[];
  options: {
    vpn?: string | null;
    cloudflare_zero_trust?: boolean;
    public_access?: boolean;
    identity_head?: "pocket_id" | "pocketbase";
    enable_pocket_id?: boolean;
    enable_pocketbase_backend?: boolean;
    enable_pocketbase?: boolean;
    enable_headscale?: boolean;
    enable_monitoring?: boolean;
    enable_traefik?: boolean;
    identity?: IdentityConfig;
    identity_provider?: string;
    tool_identity_provider?: string;
    homelab_identity_provider?: string;
    requires_passkeys?: boolean;
    identity_backend_capability?: string;
    auto_updates?: boolean;
    backups?: boolean;
    multi_user?: boolean;
    owner_bootstrap_mode?: OwnerBootstrapMode;
    owner_source?: OwnerSource;
    owner_email?: string;
    owner_username?: string;
    owner_display_name?: string;
    recovery_passphrase_hash?: string;
    recovery_material_ref?: string;
    server_provisioning_mode?: string;
    server_connection_mode?: string;
    stackkit_foundation?: string;
    server_node_role?: string;
    server_node_role_wire?: string;
    server_remote_host?: string;
    server_remote_port?: number;
    server_remote_user?: string;
    server_remote_auth_method?: string;
    server_remote_ssh_key_label?: string;
    server_remote_use_sudo?: boolean;
    server_install_command_required?: boolean;
    server_mode?: string;
    runtime_lane?: string;
    runtime_offering_id?: string;
    provider_id?: string;
    ionos_datacenter?: string;
    provider_region?: string;
    simulate_node_lifecycle?: string;
    desired_state?: string;
    billing_mode?: string;
    billing_cadence?: string;
    stackkit_catalog_ref?: string;
    verification_status?: string;
    use_cases?: string[];
  };
}

type StackConfigWithLegacyIdentity = Omit<
  StackConfig,
  "identity" | "services"
> & {
  identity?: Partial<IdentityConfig>;
  services: ServicesConfig & { pocketId?: boolean };
};

/**
 * Build API payload from unified StackConfig
 */
export function buildPayload(config: StackConfig): CreateStackPayload {
  const identity = deriveIdentityServices(config);
  const serverProvisioning = normalizeServerProvisioningConfig(config);
  const usesManagedRuntime =
    serverProvisioning.mode === "kombify-cloud" &&
    isManagedServerMode(config.serverMode);
  const usesIONOSManagedRuntime =
    usesManagedRuntime && config.providerId === "ionos";
  const usesAutoOwnerBootstrap = config.owner.bootstrapMode === "auto";
  // Cloud-linked owners derive their identity server-side from the verified
  // kombify Cloud link; client-supplied owner fields are rejected fail-closed.
  const usesCloudLinkedOwner = config.owner.source === "cloud-linked";
  const usesRemoteSSH = serverProvisioning.mode === "connect-remote";

  const services = buildPayloadServicesFromBundle(config.services);
  const useCases = selectedCanonicalUseCasesFromGoals(config.goals);

  // Determine multi-user mode
  const multiUser = config.audience.familyFriends || config.audience.public;

  // Build VPN setting
  let vpn: string | null = null;
  if (config.network.accessMode === "anywhere") {
    vpn = config.network.vpn !== "none" ? config.network.vpn : null;
  }

  return {
    name: config.name || "homelab",
    provider: config.provider,
    services,
    options: {
      vpn,
      cloudflare_zero_trust: config.network.enableCloudflare,
      public_access: config.network.publicAccess,
      identity_head: toApiIdentityHead(identity.homelabProvider),
      enable_pocket_id: config.services.pocketId,
      enable_pocketbase_backend: config.services.pocketbase,
      enable_pocketbase: config.services.pocketbase,
      enable_headscale: config.services.headscale,
      enable_monitoring: config.services.monitoring,
      enable_traefik: config.services.traefik,
      identity,
      identity_provider: identity.toolProvider,
      tool_identity_provider: identity.toolProvider,
      homelab_identity_provider: identity.homelabProvider,
      requires_passkeys: identity.requiresPasskeys,
      identity_backend_capability: identity.backendCapability,
      auto_updates: config.advanced.autoUpdates,
      backups: config.advanced.backupsEnabled,
      multi_user: multiUser,
      owner_bootstrap_mode: config.owner.bootstrapMode,
      owner_source: config.owner.source,
      owner_email: usesCloudLinkedOwner
        ? undefined
        : config.owner.email || undefined,
      owner_username: usesCloudLinkedOwner
        ? undefined
        : config.owner.username || undefined,
      owner_display_name: usesCloudLinkedOwner
        ? undefined
        : config.owner.displayName || undefined,
      recovery_passphrase_hash:
        config.owner.recoveryPassphraseHash || undefined,
      recovery_material_ref: usesAutoOwnerBootstrap
        ? config.owner.recoveryMaterialRef ||
          `techstack://recovery/stacks/${config.name || "homelab"}`
        : undefined,
      server_provisioning_mode: serverProvisioning.mode,
      server_connection_mode: serverProvisioning.connectionMode,
      stackkit_foundation: serverProvisioning.stackkitFoundation,
      server_node_role: serverProvisioning.nodeRole,
      server_node_role_wire:
        serverProvisioning.nodeRole === "foundation"
          ? "standalone"
          : serverProvisioning.nodeRole,
      server_remote_host: usesRemoteSSH
        ? serverProvisioning.remote.host || undefined
        : undefined,
      server_remote_port: usesRemoteSSH
        ? serverProvisioning.remote.sshPort
        : undefined,
      server_remote_user: usesRemoteSSH
        ? serverProvisioning.remote.sshUser || undefined
        : undefined,
      server_remote_auth_method: usesRemoteSSH
        ? serverProvisioning.remote.authMethod
        : undefined,
      server_remote_ssh_key_label:
        usesRemoteSSH && serverProvisioning.remote.sshKeyLabel
          ? serverProvisioning.remote.sshKeyLabel
          : undefined,
      server_remote_use_sudo: usesRemoteSSH
        ? serverProvisioning.remote.useSudo
        : undefined,
      server_install_command_required:
        serverProvisioning.mode === "install-command" ? true : undefined,
      server_mode: config.serverMode,
      runtime_lane: usesManagedRuntime ? "monthly-runtime" : undefined,
      runtime_offering_id: usesManagedRuntime
        ? config.runtimeOfferingId
        : undefined,
      provider_id: usesManagedRuntime ? config.providerId : undefined,
      ionos_datacenter: usesIONOSManagedRuntime
        ? config.ionosDatacenter
        : undefined,
      provider_region: usesIONOSManagedRuntime
        ? config.ionosDatacenter
        : undefined,
      simulate_node_lifecycle: usesManagedRuntime ? "pvm" : undefined,
      desired_state: "running",
      billing_mode: usesManagedRuntime ? "subscription" : "local",
      billing_cadence: usesManagedRuntime ? "monthly" : undefined,
      stackkit_catalog_ref: serverProvisioning.stackkitFoundation || config.kit,
      verification_status: config.verificationStatus,
      use_cases: useCases,
    },
  };
}

export function resolveIdentityConfig(config: StackConfig): IdentityConfig {
  const provided = (config as StackConfigWithLegacyIdentity).identity;
  const requiresPasskeys =
    provided?.requiresPasskeys === true ||
    (config.auth?.allowPasswordless ?? false);
  const homelabProvider = provided?.homelabProvider ?? "pocket-id";
  const toolProvider = provided?.toolProvider ?? "pocket-id";
  const backendCapability = requiresPasskeys
    ? "passkeys"
    : homelabProvider === "pocketbase"
      ? "pocketbase"
      : "external-identity";

  return {
    ...createDefaultIdentityConfig(),
    ...provided,
    toolProvider,
    homelabProvider,
    requiresPasskeys,
    backendCapability,
  };
}

export function deriveIdentityServices(config: StackConfig): IdentityConfig {
  const legacyConfig = config as StackConfigWithLegacyIdentity;
  const hasExplicitIdentity = Boolean(legacyConfig.identity);
  const identity = resolveIdentityConfig(config);

  legacyConfig.identity = identity;
  legacyConfig.services.pocketId =
    identity.toolProvider === "pocket-id" ||
    identity.homelabProvider === "pocket-id" ||
    identity.requiresPasskeys ||
    legacyConfig.services.pocketId === true;
  legacyConfig.services.pocketbase =
    identity.homelabProvider === "pocketbase" ||
    (!hasExplicitIdentity && legacyConfig.services.pocketbase === true);

  return identity;
}

function toApiIdentityHead(provider: IdentityConfig["homelabProvider"]) {
  return provider === "pocketbase" ? "pocketbase" : "pocket_id";
}

/**
 * Derive services from easy wizard goals
 */
export function deriveServicesFromGoals(config: StackConfig): void {
  deriveIdentityServices(config);
  applyStandardBundleGoalServices(
    config.services,
    config.goals,
    config.network.accessMode,
  );
}
