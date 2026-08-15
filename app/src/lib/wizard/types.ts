/**
 * kombify-TechStack Wizard Types
 *
 * Central type definitions for the wizard system.
 * Both EasyWizard and TechieWizard produce a StackConfig as output.
 */

import {
  ACTIVE_STANDARD_BUNDLE,
  CANONICAL_USE_CASE_GOALS,
  CLOUD_STACKKIT_REF,
  LEGACY_BASE_STACKKIT_REF,
  USER_OWNED_STACKKIT_REF,
  cloneDefaultServerProvisioning,
  cloneStandardBundleDefaults,
  type CanonicalUseCaseGoalKey,
  type IonosDatacenterValue,
  type RegistryNodeRoleValue,
  type ServerProvisioningModeValue,
  type StackKitFoundationValue,
} from "./standardBundle";

/**
 * Authentication method configuration
 */
export interface AuthConfig {
  allowUsernameLogin: boolean;
  allowEmailLogin: boolean;
  requirePassword: boolean;
  requireMfa: boolean;
  mfaMethod?: "totp" | "email";
  allowPasswordless: boolean;
  requireEmailVerification: boolean;
  centralIdentity: boolean;
  sessionTimeout: boolean;
}

/**
 * Owner bootstrap configuration for initial setup
 */
export type OwnerSource = "local" | "cloud" | "cloud-linked";
export type OwnerBootstrapMode = "auto" | "custom" | "none";
export type WizardDeploymentLane = "oss" | "self-hosted" | "saas";

export interface OwnerConfig {
  bootstrapMode: OwnerBootstrapMode;
  source: OwnerSource;
  username: string;
  email: string;
  displayName: string;
  recoveryPassphraseHash: string;
  recoveryMaterialRef: string;
}

/**
 * Admin auth configuration for initial setup
 */
export interface AdminConfig {
  password: string;
}

/**
 * Network and access configuration
 */
export interface NetworkConfig {
  accessMode: "home" | "anywhere";
  vpn: "headscale" | "wireguard" | "none";
  enableCloudflare: boolean;
  publicAccess: boolean;
  reverseProxy: boolean;
}

/**
 * Services to be deployed
 */
export interface ServicesConfig {
  pocketId: boolean;
  pocketbase: boolean;
  headscale: boolean;
  monitoring: boolean;
  traefik: boolean;
  vaultwarden: boolean;
  immich: boolean;
  files: boolean;
}

export type ServerMode =
  | "monthly-runtime"
  | "managed-cloud"
  | "user-owned"
  // Legacy persisted wizard sessions used this value for user-owned rollout
  // targets. New SaaS wizard output must use "user-owned".
  | "self-hosted";
export type ManagedProviderID = "centron" | "ionos";
export type RuntimeOfferingID =
  | "monthly-runtime-standard"
  | "monthly-runtime-premium";
export type IonosDatacenter = IonosDatacenterValue;
export type StackKitFoundation = StackKitFoundationValue;
export type RegistryNodeRole = RegistryNodeRoleValue;
export type ServerProvisioningMode = ServerProvisioningModeValue;
export type ServerConnectionMode =
  | "managed-subscription"
  | "remote-ssh"
  | "agent-oneliner";
export type RemoteServerAuthMethod = "ssh-key" | "password";

export function normalizeIonosDatacenter(
  datacenter: string | undefined,
): IonosDatacenter {
  const normalized = (datacenter || "")
    .trim()
    .toLowerCase()
    .replace(/\\/g, "/");
  switch (normalized) {
    case "de/fra":
    case "de-fra":
    case "fra":
    case "frankfurt":
      return "de/fra";
    case "de/txl":
    case "de-txl":
    case "txl":
    case "berlin":
      return "de/txl";
    case "us/ewr":
    case "us-ewr":
    case "ewr":
    case "newark":
      return "us/ewr";
    default:
      return "de/fra";
  }
}

interface ApplyServerProvisioningModeOptions {
  preserveExplicitProductKit?: boolean;
}

function applyManagedRuntimeStackKitDefault(config: StackConfig): void {
  if (
    !config.serverProvisioning.stackkitFoundation ||
    config.serverProvisioning.stackkitFoundation === USER_OWNED_STACKKIT_REF ||
    config.serverProvisioning.stackkitFoundation === LEGACY_BASE_STACKKIT_REF
  ) {
    config.serverProvisioning.stackkitFoundation = CLOUD_STACKKIT_REF;
  }
  if (
    !config.kit ||
    config.kit === USER_OWNED_STACKKIT_REF ||
    config.kit === LEGACY_BASE_STACKKIT_REF
  ) {
    config.kit = CLOUD_STACKKIT_REF;
  }
}

function applyUserOwnedStackKitDefault(
  config: StackConfig,
  options: ApplyServerProvisioningModeOptions = {},
): void {
  if (
    !config.serverProvisioning.stackkitFoundation ||
    (!options.preserveExplicitProductKit &&
      config.serverProvisioning.stackkitFoundation === CLOUD_STACKKIT_REF) ||
    config.serverProvisioning.stackkitFoundation === LEGACY_BASE_STACKKIT_REF
  ) {
    config.serverProvisioning.stackkitFoundation = USER_OWNED_STACKKIT_REF;
  }
  if (
    !config.kit ||
    (!options.preserveExplicitProductKit &&
      config.kit === CLOUD_STACKKIT_REF) ||
    config.kit === LEGACY_BASE_STACKKIT_REF
  ) {
    config.kit = USER_OWNED_STACKKIT_REF;
  }
}

export interface RemoteServerConfig {
  host: string;
  sshPort: number;
  sshUser: string;
  authMethod: RemoteServerAuthMethod;
  sshKeyLabel: string;
  useSudo: boolean;
}

export interface ServerProvisioningConfig {
  mode: ServerProvisioningMode;
  connectionMode: ServerConnectionMode;
  stackkitFoundation: StackKitFoundation;
  nodeRole: RegistryNodeRole;
  remote: RemoteServerConfig;
}

export function isManagedServerMode(mode: ServerMode): boolean {
  return mode === "monthly-runtime" || mode === "managed-cloud";
}

/**
 * Identity provider configuration
 */
export type IdentityProvider = "pocket-id" | "pocketbase";

export type IdentityBackendCapability =
  | "external-identity"
  | "pocketbase"
  | "passkeys";

export interface IdentityConfig {
  toolProvider: IdentityProvider;
  homelabProvider: IdentityProvider;
  requiresPasskeys: boolean;
  backendCapability: IdentityBackendCapability;
}

/**
 * Advanced settings
 */
export interface AdvancedConfig {
  isolation: "isolated" | "shared";
  autostart: "auto" | "manual";
  backupsEnabled: boolean;
  autoUpdates: boolean;
  centralIdentity: boolean;
  rbac: boolean;
  sessionTimeout: string;
  sso: boolean;
}

/**
 * User audience configuration
 */
export interface AudienceConfig {
  onlyMe: boolean;
  familyFriends: boolean;
  public: boolean;
}

/**
 * Feature goals selected in easy wizard
 */
export type GoalsConfig = Record<CanonicalUseCaseGoalKey, boolean> & {
  everything: boolean;
};

export function selectedUseCasesFromGoals(
  goals: Partial<GoalsConfig> | undefined,
): CanonicalUseCaseGoalKey[] {
  if (!goals) return [];
  if (goals.everything) return [...CANONICAL_USE_CASE_GOALS];
  return CANONICAL_USE_CASE_GOALS.filter((goal) => goals[goal] === true);
}

/**
 * Unified stack configuration - output of both wizards
 * This is the central data structure that gets passed to the initialization module
 */
export interface StackConfig {
  // Basic info
  name: string;
  provider: "local" | "cloud";
  wizardType: "easy" | "techie";
  kit: StackKitFoundation;
  serverMode: ServerMode;
  runtimeOfferingId: RuntimeOfferingID;
  providerId: ManagedProviderID;
  ionosDatacenter: IonosDatacenter;
  verificationStatus: "pending" | "verified" | "failed";
  serverProvisioning: ServerProvisioningConfig;

  // Goals (easy mode) or direct service selection (techie mode)
  goals?: GoalsConfig;
  services: ServicesConfig;

  // Access & Network
  network: NetworkConfig;

  // Users & Audience
  audience: AudienceConfig;

  // Authentication
  identity: IdentityConfig;
  auth: AuthConfig;
  owner: OwnerConfig;
  admin: AdminConfig;

  // Advanced options
  advanced: AdvancedConfig;

  // Discovered nodes (from network discovery)
  discoveredNodes?: DiscoveredNodeConfig[];
}

/**
 * Discovered node configuration for wizard integration
 */
export interface DiscoveredNodeConfig {
  deviceId: string;
  ip: string;
  hostname?: string;
  mac?: string;
  role: "main" | "worker" | "storage";
  sshUser?: string;
  sshPort?: number;
  services?: string[];
  system?: {
    os?: string;
    distribution?: string;
    cpuCores?: number;
    memoryMb?: number;
    diskGb?: number;
    dockerStatus?: string;
  };
}

/**
 * Wizard step definition
 */
export interface WizardStep {
  id: number;
  key: string;
  label: string;
  labelKey?: string; // i18n key for future
}

/**
 * Easy wizard steps
 * NOTE: Requirements are shown AFTER stack creation on the /stacks/creating page,
 * not as part of the wizard. The Unifier runs post-wizard.
 */
export const EASY_STEPS: WizardStep[] = [
  ...ACTIVE_STANDARD_BUNDLE.wizard.steps.easy,
];

/**
 * Techie wizard steps
 * NOTE: Requirements are shown AFTER stack creation on the /stacks/creating page,
 * not as part of the wizard. The Unifier runs post-wizard.
 */
export const TECHIE_STEPS: WizardStep[] = [
  ...ACTIVE_STANDARD_BUNDLE.wizard.steps.techie,
];

/**
 * Default stack configuration
 */
export function createDefaultConfig(
  lane: WizardDeploymentLane = "self-hosted",
): StackConfig {
  const config = cloneStandardBundleDefaults() as StackConfig;
  return applyWizardDeploymentLane(config, lane);
}

export function createDefaultServerProvisioningConfig(): ServerProvisioningConfig {
  return cloneDefaultServerProvisioning() as ServerProvisioningConfig;
}

export function applyServerProvisioningMode(
  config: StackConfig,
  mode: ServerProvisioningMode,
  options: ApplyServerProvisioningModeOptions = {},
): void {
  if (!config.serverProvisioning) {
    config.serverProvisioning = createDefaultServerProvisioningConfig();
  }
  config.serverProvisioning.stackkitFoundation ||=
    config.kit || USER_OWNED_STACKKIT_REF;
  config.serverProvisioning.nodeRole ||= "foundation";
  config.serverProvisioning.mode = mode;
  switch (mode) {
    case "kombify-cloud":
      applyManagedRuntimeStackKitDefault(config);
      config.provider = "cloud";
      config.serverMode = "monthly-runtime";
      config.serverProvisioning.connectionMode = "managed-subscription";
      config.ionosDatacenter = normalizeIonosDatacenter(config.ionosDatacenter);
      if (!config.runtimeOfferingId) {
        config.runtimeOfferingId = "monthly-runtime-standard";
      }
      config.owner.bootstrapMode = "auto";
      config.owner.source = "cloud";
      config.owner.username = "";
      config.owner.email = "";
      config.owner.displayName = "";
      config.owner.recoveryPassphraseHash = "";
      config.owner.recoveryMaterialRef ||=
        "techstack://recovery/stacks/homelab";
      config.identity.toolProvider = "pocket-id";
      config.identity.homelabProvider = "pocket-id";
      config.identity.requiresPasskeys = true;
      config.identity.backendCapability = "passkeys";
      config.auth.requirePassword = false;
      config.auth.requireMfa = false;
      config.auth.allowPasswordless = false;
      break;
    case "connect-remote":
      applyUserOwnedStackKitDefault(config, options);
      config.provider = "local";
      config.serverMode = "user-owned";
      config.serverProvisioning.connectionMode = "remote-ssh";
      resetAutoCloudOwnerForUserOwnedMode(config);
      break;
    case "install-command":
      applyUserOwnedStackKitDefault(config, options);
      config.provider = "local";
      config.serverMode = "user-owned";
      config.serverProvisioning.connectionMode = "agent-oneliner";
      resetAutoCloudOwnerForUserOwnedMode(config);
      break;
  }
}

// resetAutoCloudOwnerForUserOwnedMode reverses the managed-lane owner defaults
// when the user leaves kombify-cloud provisioning: the SaaS auto "cloud" owner
// cannot seed a user-owned rollout, so the wizard falls back to a custom local
// owner. An explicit cloud-linked selection is preserved.
function resetAutoCloudOwnerForUserOwnedMode(config: StackConfig): void {
  if (config.owner.source !== "cloud") return;
  config.owner.bootstrapMode = "custom";
  config.owner.source = "local";
}

export function applyWizardDeploymentLane(
  config: StackConfig,
  lane: WizardDeploymentLane,
): StackConfig {
  switch (lane) {
    case "saas":
      applyServerProvisioningMode(config, "kombify-cloud");
      config.owner.bootstrapMode = "auto";
      config.owner.source = "cloud";
      config.owner.username = "";
      config.owner.email = "";
      config.owner.displayName = "";
      config.owner.recoveryPassphraseHash = "";
      config.owner.recoveryMaterialRef ||=
        "techstack://recovery/stacks/homelab";
      config.identity.toolProvider = "pocket-id";
      config.identity.homelabProvider = "pocket-id";
      config.identity.requiresPasskeys = true;
      config.identity.backendCapability = "passkeys";
      config.auth.requirePassword = false;
      config.auth.requireMfa = false;
      config.auth.allowPasswordless = false;
      return config;
    case "oss":
    case "self-hosted":
      applyServerProvisioningMode(config, "install-command");
      config.owner.bootstrapMode ||= "custom";
      config.owner.source = "local";
      return config;
  }
}

export function normalizeServerProvisioningConfig(
  config: StackConfig,
): ServerProvisioningConfig {
  if (!config.serverProvisioning) {
    config.serverProvisioning = createDefaultServerProvisioningConfig();
  }
  config.serverProvisioning.stackkitFoundation ||=
    config.kit || USER_OWNED_STACKKIT_REF;
  config.serverProvisioning.nodeRole ||= "foundation";
  applyServerProvisioningMode(config, config.serverProvisioning.mode, {
    preserveExplicitProductKit: true,
  });
  return config.serverProvisioning;
}

export function createDefaultIdentityConfig(): IdentityConfig {
  return cloneStandardBundleDefaults().identity as IdentityConfig;
}
