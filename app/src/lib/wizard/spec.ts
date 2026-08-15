import type { StackConfig } from "./types";
import { deriveIdentityServices } from "./payload";
import {
  ACTIVE_STANDARD_BUNDLE,
  buildStackSpecServiceTogglesFromBundle,
  selectedCanonicalUseCasesFromGoals,
} from "./standardBundle";
import {
  isManagedServerMode,
  normalizeServerProvisioningConfig,
} from "./types";

export interface StackKitNodeSpec {
  name: string;
  role: "standalone" | "main" | "worker" | "storage" | "control-plane";
  ip?: string;
  host?: string;
  services?: string[];
  ssh?: {
    host: string;
    port?: number;
    user: string;
  };
}

export interface StackKitServiceToggle {
  enabled: boolean;
}

export interface StackKitStackSpec {
  name: string;
  stackkit: string;
  mode: "simple" | "bootstrapped" | "advanced";
  runtime: "docker" | "native";
  context: "local" | "cloud" | "pi";
  domain: string;
  subdomainPrefix?: string;
  email?: string;
  adminEmail?: string;
  network: {
    mode: "local" | "public" | "hybrid";
    subnet?: string;
  };
  compute: {
    tier: "low" | "standard" | "high";
  };
  ssh: {
    user: string;
    port: number;
  };
  nodes: StackKitNodeSpec[];
  paas: "dokploy" | "coolify";
  useCases: string[];
  addons: string[];
  services: Record<string, StackKitServiceToggle>;
  owner: {
    bootstrapMode: StackConfig["owner"]["bootstrapMode"];
    source: StackConfig["owner"]["source"];
    email?: string;
    username?: string;
    displayName?: string;
    recoveryPassphraseHash?: string;
    recoveryMaterialRef?: string;
  };
  identity: {
    oidcProvider: "pocketid" | "external";
    ldapEnabled?: boolean;
  };
  vpn?: {
    enabled: boolean;
    type: "headscale" | "wireguard" | "none";
  };
  metadata: Record<string, string>;
}

const MAIN_NODE = "main";
const DEFAULT_STACK_NAME = "homelab";
const DEFAULT_LOCAL_DOMAIN = "stack.home";
const DEFAULT_MANAGED_DOMAIN = "kombify.me";
const DECISION_CONTEXT_VERSION = "techstack/decision-context/v1";

function isReservedStackName(name: string): boolean {
  const normalized = name.trim().toLowerCase();
  return (
    normalized === "techstack" ||
    normalized.startsWith("techstack-") ||
    normalized === "kombify-techstack" ||
    normalized.startsWith("kombify-techstack-") ||
    normalized === "kombifytechstack" ||
    normalized.startsWith("kombifytechstack-")
  );
}

function sanitizeStackName(name: string): string {
  const sanitized = name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .replace(/-{2,}/g, "-");
  const candidate = sanitized || DEFAULT_STACK_NAME;
  const prefixed = /^[a-z]/.test(candidate) ? candidate : `stack-${candidate}`;
  return isReservedStackName(prefixed) ? DEFAULT_STACK_NAME : prefixed;
}

function isIPv4(value: string): boolean {
  const parts = value.trim().split(".");
  if (parts.length !== 4) return false;
  return parts.every((part) => {
    if (!/^\d+$/.test(part)) return false;
    const n = Number(part);
    return n >= 0 && n <= 255;
  });
}

function buildContext(config: StackConfig): StackKitStackSpec["context"] {
  if (usesManagedRuntime(config)) return "cloud";
  return config.provider === "cloud" && isManagedServerMode(config.serverMode)
    ? "cloud"
    : "local";
}

function buildNetworkMode(
  config: StackConfig,
): StackKitStackSpec["network"]["mode"] {
  if (usesManagedRuntime(config)) return "public";
  if (config.network.accessMode === "home") return "local";
  return buildContext(config) === "cloud" ? "public" : "hybrid";
}

function buildDomain(config: StackConfig): string {
  if (usesManagedRuntime(config)) return DEFAULT_MANAGED_DOMAIN;
  if (buildContext(config) === "cloud") return DEFAULT_MANAGED_DOMAIN;
  return DEFAULT_LOCAL_DOMAIN;
}

function usesManagedRuntime(config: StackConfig): boolean {
  const serverProvisioning = config.serverProvisioning;
  return (
    config.provider === "cloud" &&
    serverProvisioning?.mode === "kombify-cloud" &&
    isManagedServerMode(config.serverMode)
  );
}

function buildSubdomainPrefix(
  _name: string,
  _domain: string,
): string | undefined {
  return undefined;
}

function buildAdminEmail(
  ownerEmail: string,
  domain: string,
  subdomainPrefix?: string,
): string | undefined {
  const trimmed = ownerEmail.trim();
  if (trimmed) return trimmed;
  if (domain === DEFAULT_MANAGED_DOMAIN) return undefined;
  const host =
    domain === DEFAULT_MANAGED_DOMAIN && subdomainPrefix
      ? `${subdomainPrefix}.${domain}`
      : domain;
  return `admin@${host}`;
}

function buildRecoveryMaterialRef(config: StackConfig, name: string): string {
  return (
    config.owner.recoveryMaterialRef || `techstack://recovery/stacks/${name}`
  );
}

function buildComputeTier(
  config: StackConfig,
): StackKitStackSpec["compute"]["tier"] {
  if (config.runtimeOfferingId === "monthly-runtime-premium") return "high";
  return "standard";
}

function buildPAAS(config: StackConfig): StackKitStackSpec["paas"] {
  if (usesManagedRuntime(config)) return "coolify";
  const context = buildContext(config);
  if (
    context === "cloud" &&
    config.serverProvisioning.mode === "kombify-cloud"
  ) {
    return "coolify";
  }
  return "dokploy";
}

function buildAddons(config: StackConfig): string[] {
  if (
    config.network.accessMode === "anywhere" &&
    config.network.vpn !== "none"
  ) {
    return ["vpn-overlay"];
  }
  return [];
}

function clampCapabilityScore(score: number): number {
  return Math.max(1, Math.min(10, score));
}

function operatorCapabilityBand(score: number): string {
  if (score <= 2) return "oneclick";
  if (score <= 4) return "guided";
  if (score <= 6) return "curious-admin";
  if (score <= 8) return "techie";
  return "expert";
}

function countAdvancedInteractions(config: StackConfig): number {
  const defaults = ACTIVE_STANDARD_BUNDLE.defaults
    .advanced as StackConfig["advanced"];
  return (Object.keys(defaults) as Array<keyof StackConfig["advanced"]>).reduce(
    (count, key) =>
      config.advanced[key] !== defaults[key] ? count + 1 : count,
    0,
  );
}

function countExplicitAlternatives(config: StackConfig): number {
  const defaults = ACTIVE_STANDARD_BUNDLE.defaults;
  let count = 0;

  if (config.serverProvisioning.mode !== defaults.serverProvisioning.mode)
    count += 1;
  if (
    config.serverProvisioning.stackkitFoundation !==
    defaults.serverProvisioning.stackkitFoundation
  )
    count += 1;
  if (
    config.serverProvisioning.nodeRole !== defaults.serverProvisioning.nodeRole
  )
    count += 1;
  if (config.network.accessMode !== defaults.network.accessMode) count += 1;
  if (config.network.vpn !== defaults.network.vpn) count += 1;
  if (config.identity.homelabProvider !== defaults.identity.homelabProvider)
    count += 1;
  if (
    Object.entries(defaults.services).some(([key, value]) => {
      const serviceKey = key as keyof StackConfig["services"];
      return config.services[serviceKey] !== value;
    })
  )
    count += 1;

  return count;
}

function operatorCapabilityScore(
  config: StackConfig,
  advancedInteractionCount: number,
  explicitAlternativeCount: number,
): number {
  const baseline =
    config.wizardType === "techie"
      ? 7
      : config.owner.source === "cloud"
        ? 1
        : 3;
  const advancedLift = Math.min(2, advancedInteractionCount);
  const alternativeLift = explicitAlternativeCount > 0 ? 1 : 0;
  return clampCapabilityScore(baseline + advancedLift + alternativeLift);
}

function operatorCapabilityConfidence(
  config: StackConfig,
  advancedInteractionCount: number,
  explicitAlternativeCount: number,
): string {
  const baseline = config.wizardType === "techie" ? 0.7 : 0.55;
  const confidence = Math.min(
    0.9,
    baseline +
      advancedInteractionCount * 0.03 +
      explicitAlternativeCount * 0.02,
  );
  return confidence.toFixed(2);
}

function operatorCapabilityEvidence(
  config: StackConfig,
  advancedInteractionCount: number,
  explicitAlternativeCount: number,
): string {
  const evidence = [`wizard:${config.wizardType}`];
  if (advancedInteractionCount > 0)
    evidence.push(`advanced:${advancedInteractionCount}`);
  if (explicitAlternativeCount > 0)
    evidence.push(`alternatives:${explicitAlternativeCount}`);
  if (config.serverProvisioning.mode === "connect-remote")
    evidence.push("remote-ssh");
  if (config.serverProvisioning.mode === "kombify-cloud")
    evidence.push("managed-runtime");
  return evidence.join(",");
}

function buildServices(config: StackConfig): StackKitStackSpec["services"] {
  const services: StackKitStackSpec["services"] = {};
  const toggles = buildStackSpecServiceTogglesFromBundle(config.services);
  for (const [name, enabled] of Object.entries(toggles)) {
    services[name] = { enabled };
  }

  const paas = buildPAAS(config);
  const paasServices = ACTIVE_STANDARD_BUNDLE.stackSpec.paasServices;
  services[paasServices.dokploy] = { enabled: paas === "dokploy" };
  if (paas === "coolify") {
    services[paasServices.coolify] = { enabled: true };
  }

  return services;
}

function buildSSH(
  serverProvisioning: ReturnType<typeof normalizeServerProvisioningConfig>,
): StackKitStackSpec["ssh"] {
  return {
    user: serverProvisioning.remote.sshUser.trim() || "root",
    port: serverProvisioning.remote.sshPort || 22,
  };
}

function stackSpecRole(
  serverProvisioning: ReturnType<typeof normalizeServerProvisioningConfig>,
): StackKitNodeSpec["role"] {
  switch (serverProvisioning.nodeRole) {
    case "worker":
      return "worker";
    case "storage":
      return "storage";
    case "foundation":
    default:
      return "standalone";
  }
}

function stackSpecNodeName(
  serverProvisioning: ReturnType<typeof normalizeServerProvisioningConfig>,
): string {
  switch (serverProvisioning.nodeRole) {
    case "worker":
      return "worker-1";
    case "storage":
      return "storage-1";
    case "foundation":
    default:
      return MAIN_NODE;
  }
}

function buildNode(
  serverProvisioning: ReturnType<typeof normalizeServerProvisioningConfig>,
): StackKitNodeSpec {
  const node: StackKitNodeSpec = {
    name: stackSpecNodeName(serverProvisioning),
    role: stackSpecRole(serverProvisioning),
  };

  if (
    serverProvisioning.mode === "connect-remote" &&
    serverProvisioning.remote.host.trim()
  ) {
    node.ssh = {
      host: serverProvisioning.remote.host.trim(),
      port: serverProvisioning.remote.sshPort,
      user: serverProvisioning.remote.sshUser.trim() || "root",
    };
    node.ip = node.ssh.host;
    if (!isIPv4(node.ssh.host)) {
      node.host = node.ssh.host;
    }
  }

  return node;
}

/**
 * Build the StackKits stack-spec from the Wizard StackConfig.
 *
 * This is the user-owned spec shape StackKits consume as stack-spec.yaml.
 * Easy and Techie Wizard both converge here before the backend sees the config.
 */
export function buildStackKitSpecFromStackConfig(
  config: StackConfig,
): StackKitStackSpec {
  const identity = deriveIdentityServices(config);
  const serverProvisioning = normalizeServerProvisioningConfig(config);
  const usesManagedRuntime =
    serverProvisioning.mode === "kombify-cloud" &&
    isManagedServerMode(config.serverMode);
  const usesIONOSManagedRuntime =
    usesManagedRuntime && config.providerId === "ionos";
  const usesRemoteSSH = serverProvisioning.mode === "connect-remote";
  const recoveryPassphraseHashPresent = Boolean(
    config.owner.recoveryPassphraseHash,
  );
  const name = sanitizeStackName(config.name || DEFAULT_STACK_NAME);
  const domain = buildDomain(config);
  const subdomainPrefix = buildSubdomainPrefix(name, domain);
  const ownerBootstrapMode = usesManagedRuntime
    ? "auto"
    : config.owner.bootstrapMode;
  const ownerSource =
    ownerBootstrapMode === "auto" ? "cloud" : config.owner.source;
  const adminEmail =
    ownerBootstrapMode === "auto"
      ? undefined
      : buildAdminEmail(config.owner.email, domain, subdomainPrefix);
  const context = buildContext(config);
  const vpnEnabled =
    config.network.accessMode === "anywhere" && config.network.vpn !== "none";
  const stackkitFoundation =
    serverProvisioning.stackkitFoundation || config.kit;
  const stackSpecNodeRole = stackSpecRole(serverProvisioning);
  const advancedInteractionCount = countAdvancedInteractions(config);
  const explicitAlternativeCount = countExplicitAlternatives(config);
  const capabilityScore = operatorCapabilityScore(
    config,
    advancedInteractionCount,
    explicitAlternativeCount,
  );
  const useCases = selectedCanonicalUseCasesFromGoals(config.goals);

  return {
    name,
    stackkit: stackkitFoundation,
    mode: usesManagedRuntime ? "bootstrapped" : "simple",
    runtime: "docker",
    context,
    domain,
    subdomainPrefix,
    email: adminEmail,
    adminEmail,
    network: {
      mode: buildNetworkMode(config),
    },
    compute: {
      tier: buildComputeTier(config),
    },
    ssh: buildSSH(serverProvisioning),
    nodes: [buildNode(serverProvisioning)],
    paas: buildPAAS(config),
    useCases,
    addons: buildAddons(config),
    services: buildServices(config),
    owner: {
      bootstrapMode: ownerBootstrapMode,
      source: ownerSource,
      // Cloud-linked owners derive identity server-side; sending local fields
      // alongside would be rejected fail-closed by the backend.
      email:
        ownerSource === "cloud-linked"
          ? undefined
          : config.owner.email || undefined,
      username:
        ownerSource === "cloud-linked"
          ? undefined
          : config.owner.username || undefined,
      displayName:
        ownerSource === "cloud-linked"
          ? undefined
          : config.owner.displayName || undefined,
      recoveryMaterialRef:
        ownerBootstrapMode === "auto"
          ? buildRecoveryMaterialRef(config, name)
          : undefined,
    },
    identity: {
      oidcProvider:
        identity.homelabProvider === "pocket-id" ? "pocketid" : "external",
      ldapEnabled:
        identity.homelabProvider === "pocketbase" ? false : undefined,
    },
    vpn: vpnEnabled
      ? {
          enabled: true,
          type: config.network.vpn,
        }
      : undefined,
    metadata: {
      spec_format: "stack-spec",
      spec_version: "2.0",
      created_by: "wizard",
      wizard_type: config.wizardType,
      decision_context_version: DECISION_CONTEXT_VERSION,
      decision_context_source: "wizard-metadata",
      decision_channel: `wizard:${config.wizardType}`,
      operator_capability_score: String(capabilityScore),
      operator_capability_band: operatorCapabilityBand(capabilityScore),
      operator_capability_source: "wizard-derived",
      operator_capability_confidence: operatorCapabilityConfidence(
        config,
        advancedInteractionCount,
        explicitAlternativeCount,
      ),
      operator_capability_evidence: operatorCapabilityEvidence(
        config,
        advancedInteractionCount,
        explicitAlternativeCount,
      ),
      advanced_interaction_count: String(advancedInteractionCount),
      explicit_alternative_count: String(explicitAlternativeCount),
      advanced_settings_touched: String(advancedInteractionCount > 0),
      provider: config.provider,
      access_mode: config.network.accessMode,
      vpn: config.network.vpn,
      owner_bootstrap_mode: ownerBootstrapMode,
      owner_source: ownerSource,
      owner_email_present: String(Boolean(config.owner.email)),
      owner_username_present: String(Boolean(config.owner.username)),
      owner_display_name_present: String(Boolean(config.owner.displayName)),
      recovery_passphrase_hash_present: String(recoveryPassphraseHashPresent),
      recovery_material_ref_present: String(ownerBootstrapMode === "auto"),
      identity_head:
        identity.homelabProvider === "pocketbase" ? "pocketbase" : "pocket_id",
      identity_provider: identity.toolProvider,
      homelab_identity_provider: identity.homelabProvider,
      requires_passkeys: String(identity.requiresPasskeys),
      identity_backend_capability: identity.backendCapability,
      server_provisioning_mode: serverProvisioning.mode,
      server_connection_mode: serverProvisioning.connectionMode,
      address_mode: usesManagedRuntime ? "kombify-me" : "local",
      server_remote_host_present: String(
        usesRemoteSSH && Boolean(serverProvisioning.remote.host),
      ),
      server_remote_port: usesRemoteSSH
        ? String(serverProvisioning.remote.sshPort)
        : "",
      server_remote_user_present: String(
        usesRemoteSSH && Boolean(serverProvisioning.remote.sshUser),
      ),
      server_remote_auth_method: usesRemoteSSH
        ? serverProvisioning.remote.authMethod
        : "",
      server_remote_ssh_key_label: usesRemoteSSH
        ? serverProvisioning.remote.sshKeyLabel
        : "",
      server_remote_use_sudo: usesRemoteSSH
        ? String(serverProvisioning.remote.useSudo)
        : "",
      server_install_command_required: String(
        serverProvisioning.mode === "install-command",
      ),
      server_registry_module: "server-registry",
      service_registry_module: "service-registry",
      stackkit_foundation: stackkitFoundation,
      foundation_node_label: "Foundation Node",
      server_node_role: serverProvisioning.nodeRole,
      server_node_role_wire: stackSpecNodeRole,
      foundation_node_wire_compat: "main,standalone,control-plane",
      server_mode: config.serverMode,
      runtime_lane: usesManagedRuntime ? "monthly-runtime" : "",
      runtime_offering_id: usesManagedRuntime ? config.runtimeOfferingId : "",
      provider_id: usesManagedRuntime ? config.providerId : "",
      ionos_datacenter: usesIONOSManagedRuntime ? config.ionosDatacenter : "",
      provider_region: usesIONOSManagedRuntime ? config.ionosDatacenter : "",
      simulate_node_lifecycle: usesManagedRuntime ? "pvm" : "",
      desired_state: "running",
      billing_mode: usesManagedRuntime ? "subscription" : "local",
      billing_cadence: usesManagedRuntime ? "monthly" : "",
      stackkit_catalog_ref: stackkitFoundation,
      use_cases: useCases.join(","),
      verification_status: config.verificationStatus,
    },
  };
}

/**
 * Backward-compatible export for existing preview callers.
 * It now returns the same StackKits stack-spec used for stack creation.
 */
export function buildInputSpecFromStackConfig(
  config: StackConfig,
): StackKitStackSpec {
  return buildStackKitSpecFromStackConfig(config);
}
