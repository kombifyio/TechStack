/**
 * Active StandardBundle fixture for the release wizard.
 *
 * This is the documented read-only fallback until the StackKits registry/API
 * serves the same shape. Wizard screens, defaults, payload service names, and
 * StackKit service specs should read from here instead of carrying local Base
 * Kit lists.
 */

export type WizardSurface = "easy" | "techie";

export interface WizardStepDefinition {
  id: number;
  key: string;
  label: string;
  labelKey?: string;
}

export const CANONICAL_USE_CASE_GOALS = [
  "photos",
  "media",
  "vault",
  "files",
  "smart-home",
  "ai",
  "dev",
  "mail",
  "game",
] as const;

export type CanonicalUseCaseGoalKey = (typeof CANONICAL_USE_CASE_GOALS)[number];
export type GoalConfigKey = CanonicalUseCaseGoalKey | "everything";
export type GoalSurface = "primary" | "advanced";

export const PRIMARY_USE_CASE_GOALS = [
  "photos",
  "media",
  "vault",
  "files",
] as const satisfies readonly CanonicalUseCaseGoalKey[];

export const ADVANCED_USE_CASE_GOALS = [
  "smart-home",
  "ai",
  "dev",
  "mail",
  "game",
] as const satisfies readonly CanonicalUseCaseGoalKey[];

export const USE_CASE_FEATURE_FLAGS = {
  photos: "use_case_photos",
  media: "use_case_media",
  vault: "use_case_vault",
  files: "use_case_files",
  "smart-home": "use_case_smart_home",
  ai: "use_case_ai",
  dev: "use_case_dev",
  mail: "use_case_mail",
  game: "use_case_game",
} as const satisfies Record<CanonicalUseCaseGoalKey, string>;
export type AudienceConfigKey = "onlyMe" | "familyFriends" | "public";
export type AccessModeValue = "home" | "anywhere";
export type VpnValue = "headscale" | "wireguard" | "none";
export type StackKitFoundationValue = "basement-kit" | "cloud-kit" | "base-kit";
export type RegistryNodeRoleValue = "foundation" | "worker" | "storage";
export type ServerProvisioningModeValue =
  | "kombify-cloud"
  | "connect-remote"
  | "install-command";
export type ManagedProviderIDValue = "centron" | "ionos";
export type IonosDatacenterValue = "de/fra" | "de/txl" | "us/ewr";

export type StandardBundleServiceKey =
  | "pocketId"
  | "pocketbase"
  | "headscale"
  | "monitoring"
  | "traefik"
  | "vaultwarden"
  | "immich"
  | "files";

export type StandardBundleServiceSelections = Partial<
  Record<StandardBundleServiceKey, boolean>
>;

export interface BundleQuestionDefinition<TKey extends string = string> {
  key: string;
  configKey: TKey;
  surface?: GoalSurface;
  featureFlag?: string;
  titleKey: string;
  descriptionKey: string;
  helpKey?: string;
  tipKey?: string;
  testId: string;
}

export interface BundleChoiceDefinition<TValue extends string = string> {
  value: TValue;
  label?: string;
  description?: string;
  titleKey: string;
  descriptionKey: string;
  helpKey?: string;
  tipKey?: string;
  badgeKey?: string;
  testId: string;
}

export interface BundleRegistryChoiceDefinition<
  TValue extends string = string,
> {
  value: TValue;
  label: string;
  description: string;
  testId: string;
}

export interface StackKitServiceDefinition {
  name: string;
  type: string;
  needs?: string[];
}

export interface BundleServiceDefinition {
  key: StandardBundleServiceKey;
  label: string;
  description: string;
  helpText: string;
  testId: string;
  defaultSelected: boolean;
  selectableInTechie: boolean;
  payloadServices: string[];
  stackSpecServices: string[];
  stackKitServices: StackKitServiceDefinition[];
}

export interface StandardBundleDefinition {
  id: string;
  version: string;
  kit: "basement-kit";
  releaseLabel: string;
  defaults: {
    name: string;
    provider: "local" | "cloud";
    wizardType: "easy";
    kit: "basement-kit";
    serverMode: "user-owned" | "monthly-runtime" | "managed-cloud";
    runtimeOfferingId: "monthly-runtime-standard";
    providerId: ManagedProviderIDValue;
    ionosDatacenter: IonosDatacenterValue;
    verificationStatus: "pending";
    serverProvisioning: {
      mode: ServerProvisioningModeValue;
      connectionMode: "agent-oneliner" | "remote-ssh" | "managed-subscription";
      stackkitFoundation: StackKitFoundationValue;
      nodeRole: RegistryNodeRoleValue;
      remote: {
        host: string;
        sshPort: number;
        sshUser: string;
        authMethod: "ssh-key";
        sshKeyLabel: string;
        useSudo: boolean;
      };
    };
    goals: Record<GoalConfigKey, boolean>;
    services: Record<StandardBundleServiceKey, boolean>;
    network: {
      accessMode: AccessModeValue;
      vpn: "headscale";
      enableCloudflare: boolean;
      publicAccess: boolean;
      reverseProxy: boolean;
    };
    audience: Record<AudienceConfigKey, boolean>;
    identity: {
      toolProvider: "pocket-id";
      homelabProvider: "pocket-id";
      requiresPasskeys: boolean;
      backendCapability: "external-identity";
    };
    auth: {
      allowUsernameLogin: boolean;
      allowEmailLogin: boolean;
      requirePassword: boolean;
      requireMfa: boolean;
      mfaMethod: "totp";
      allowPasswordless: boolean;
      requireEmailVerification: boolean;
      centralIdentity: boolean;
      sessionTimeout: boolean;
    };
    owner: {
      bootstrapMode: "custom";
      source: "local";
      username: string;
      email: string;
      displayName: string;
      recoveryPassphraseHash: string;
      recoveryMaterialRef: string;
    };
    admin: {
      password: string;
    };
    advanced: {
      isolation: "isolated";
      autostart: "auto";
      backupsEnabled: boolean;
      autoUpdates: boolean;
      centralIdentity: boolean;
      rbac: boolean;
      sessionTimeout: string;
      sso: boolean;
    };
  };
  wizard: {
    steps: Record<WizardSurface, WizardStepDefinition[]>;
    discovery: Record<WizardSurface, boolean>;
    goals: BundleQuestionDefinition<GoalConfigKey>[];
    access: BundleChoiceDefinition<AccessModeValue>[];
    audience: BundleQuestionDefinition<AudienceConfigKey>[];
    vpn: BundleChoiceDefinition<VpnValue>[];
    serverProvisioningModes: BundleChoiceDefinition<ServerProvisioningModeValue>[];
    ionosDatacenters: BundleRegistryChoiceDefinition<IonosDatacenterValue>[];
    stackkitFoundations: BundleRegistryChoiceDefinition<StackKitFoundationValue>[];
    nodeRoles: BundleRegistryChoiceDefinition<RegistryNodeRoleValue>[];
    goalServiceMap: Record<
      GoalConfigKey | "baseline" | "accessAnywhere",
      StandardBundleServiceKey[]
    >;
  };
  stackSpec: {
    coreServices: string[];
    paasServices: Record<"dokploy" | "coolify", string>;
  };
  services: BundleServiceDefinition[];
}

const mainNode = "main";
export const USER_OWNED_STACKKIT_REF = "basement-kit";
export const CLOUD_STACKKIT_REF = "cloud-kit";
export const LEGACY_BASE_STACKKIT_REF = "base-kit";

function stackService(
  name: string,
  type: string,
  needs?: string[],
): StackKitServiceDefinition {
  return needs && needs.length > 0 ? { name, type, needs } : { name, type };
}

export const ACTIVE_STANDARD_BUNDLE: StandardBundleDefinition = {
  id: "basement-kit.standard.v1",
  version: "1.0.0",
  kit: "basement-kit",
  releaseLabel: "Basement Kit standard release",
  defaults: {
    name: "homelab",
    provider: "local",
    wizardType: "easy",
    kit: "basement-kit",
    serverMode: "user-owned",
    runtimeOfferingId: "monthly-runtime-standard",
    providerId: "centron",
    ionosDatacenter: "de/fra",
    verificationStatus: "pending",
    serverProvisioning: {
      mode: "install-command",
      connectionMode: "agent-oneliner",
      stackkitFoundation: "basement-kit",
      nodeRole: "foundation",
      remote: {
        host: "",
        sshPort: 22,
        sshUser: "root",
        authMethod: "ssh-key",
        sshKeyLabel: "",
        useSudo: false,
      },
    },
    goals: {
      "smart-home": false,
      photos: false,
      media: false,
      vault: true,
      files: false,
      ai: false,
      dev: false,
      mail: false,
      game: false,
      everything: false,
    },
    services: {
      pocketId: true,
      pocketbase: false,
      headscale: false,
      monitoring: true,
      traefik: true,
      vaultwarden: true,
      immich: false,
      files: false,
    },
    network: {
      accessMode: "anywhere",
      vpn: "headscale",
      enableCloudflare: false,
      publicAccess: false,
      reverseProxy: true,
    },
    audience: {
      onlyMe: true,
      familyFriends: false,
      public: false,
    },
    identity: {
      toolProvider: "pocket-id",
      homelabProvider: "pocket-id",
      requiresPasskeys: false,
      backendCapability: "external-identity",
    },
    auth: {
      allowUsernameLogin: true,
      allowEmailLogin: true,
      requirePassword: false,
      requireMfa: false,
      mfaMethod: "totp",
      allowPasswordless: false,
      requireEmailVerification: false,
      centralIdentity: true,
      sessionTimeout: true,
    },
    owner: {
      bootstrapMode: "custom",
      source: "local",
      username: "",
      email: "",
      displayName: "",
      recoveryPassphraseHash: "",
      recoveryMaterialRef: "",
    },
    admin: {
      password: "",
    },
    advanced: {
      isolation: "isolated",
      autostart: "auto",
      backupsEnabled: true,
      autoUpdates: true,
      centralIdentity: true,
      rbac: true,
      sessionTimeout: "30",
      sso: true,
    },
  },
  wizard: {
    steps: {
      easy: [
        { id: 1, key: "goals", label: "Goals", labelKey: "wizard.step.goals" },
        {
          id: 2,
          key: "server",
          label: "Server",
          labelKey: "wizard.step.server",
        },
        {
          id: 3,
          key: "access",
          label: "Access",
          labelKey: "wizard.step.access",
        },
        { id: 4, key: "users", label: "Users", labelKey: "wizard.step.users" },
        { id: 5, key: "login", label: "Login", labelKey: "wizard.step.login" },
      ],
      techie: [
        {
          id: 1,
          key: "server",
          label: "Server",
          labelKey: "wizard.step.server",
        },
        {
          id: 2,
          key: "network",
          label: "Network",
          labelKey: "wizard.step.network",
        },
        {
          id: 3,
          key: "services",
          label: "Services",
          labelKey: "wizard.step.services",
        },
        {
          id: 4,
          key: "review",
          label: "Review",
          labelKey: "wizard.step.review",
        },
      ],
    },
    discovery: {
      easy: false,
      techie: false,
    },
    goals: [
      {
        key: "photos",
        configKey: "photos",
        surface: "primary",
        featureFlag: USE_CASE_FEATURE_FLAGS.photos,
        titleKey: "wizard.goals.photos.title",
        descriptionKey: "wizard.goals.photos.description",
        helpKey: "wizard.goals.photos.help",
        tipKey: "wizard.goals.photos.tip",
        testId: "easy-feature-storage",
      },
      {
        key: "media",
        configKey: "media",
        surface: "primary",
        featureFlag: USE_CASE_FEATURE_FLAGS.media,
        titleKey: "wizard.goals.media.title",
        descriptionKey: "wizard.goals.media.description",
        helpKey: "wizard.goals.media.help",
        tipKey: "wizard.goals.media.tip",
        testId: "easy-feature-media",
      },
      {
        key: "vault",
        configKey: "vault",
        surface: "primary",
        featureFlag: USE_CASE_FEATURE_FLAGS.vault,
        titleKey: "wizard.goals.vault.title",
        descriptionKey: "wizard.goals.vault.description",
        helpKey: "wizard.goals.vault.help",
        tipKey: "wizard.goals.vault.tip",
        testId: "easy-feature-vault",
      },
      {
        key: "files",
        configKey: "files",
        surface: "primary",
        featureFlag: USE_CASE_FEATURE_FLAGS.files,
        titleKey: "wizard.goals.files.title",
        descriptionKey: "wizard.goals.files.description",
        helpKey: "wizard.goals.files.help",
        tipKey: "wizard.goals.files.tip",
        testId: "easy-feature-files",
      },
      {
        key: "everything",
        configKey: "everything",
        surface: "primary",
        titleKey: "wizard.goals.everything.title",
        descriptionKey: "wizard.goals.everything.description",
        helpKey: "wizard.goals.everything.help",
        tipKey: "wizard.goals.everything.tip",
        testId: "easy-feature-everything",
      },
      {
        key: "smart-home",
        configKey: "smart-home",
        surface: "advanced",
        featureFlag: USE_CASE_FEATURE_FLAGS["smart-home"],
        titleKey: "wizard.goals.smartHome.title",
        descriptionKey: "wizard.goals.smartHome.description",
        helpKey: "wizard.goals.smartHome.help",
        tipKey: "wizard.goals.smartHome.tip",
        testId: "easy-feature-smart-home",
      },
      {
        key: "ai",
        configKey: "ai",
        surface: "advanced",
        featureFlag: USE_CASE_FEATURE_FLAGS.ai,
        titleKey: "wizard.goals.ai.title",
        descriptionKey: "wizard.goals.ai.description",
        helpKey: "wizard.goals.ai.help",
        tipKey: "wizard.goals.ai.tip",
        testId: "easy-feature-ai",
      },
      {
        key: "dev",
        configKey: "dev",
        surface: "advanced",
        featureFlag: USE_CASE_FEATURE_FLAGS.dev,
        titleKey: "wizard.goals.dev.title",
        descriptionKey: "wizard.goals.dev.description",
        helpKey: "wizard.goals.dev.help",
        tipKey: "wizard.goals.dev.tip",
        testId: "easy-feature-dev",
      },
      {
        key: "mail",
        configKey: "mail",
        surface: "advanced",
        featureFlag: USE_CASE_FEATURE_FLAGS.mail,
        titleKey: "wizard.goals.mail.title",
        descriptionKey: "wizard.goals.mail.description",
        helpKey: "wizard.goals.mail.help",
        tipKey: "wizard.goals.mail.tip",
        testId: "easy-feature-website",
      },
      {
        key: "game",
        configKey: "game",
        surface: "advanced",
        featureFlag: USE_CASE_FEATURE_FLAGS.game,
        titleKey: "wizard.goals.game.title",
        descriptionKey: "wizard.goals.game.description",
        helpKey: "wizard.goals.game.help",
        tipKey: "wizard.goals.game.tip",
        testId: "easy-feature-game",
      },
    ],
    access: [
      {
        value: "home",
        titleKey: "wizard.access.home.title",
        descriptionKey: "wizard.access.home.description",
        helpKey: "wizard.access.home.help",
        tipKey: "wizard.access.home.tip",
        testId: "easy-access-home",
      },
      {
        value: "anywhere",
        titleKey: "wizard.access.anywhere.title",
        descriptionKey: "wizard.access.anywhere.description",
        helpKey: "wizard.access.anywhere.help",
        tipKey: "wizard.access.anywhere.tip",
        testId: "easy-access-anywhere",
      },
    ],
    audience: [
      {
        key: "only-me",
        configKey: "onlyMe",
        titleKey: "wizard.users.me.title",
        descriptionKey: "wizard.users.me.description",
        helpKey: "wizard.users.me.help",
        tipKey: "wizard.users.me.tip",
        testId: "easy-users-me",
      },
      {
        key: "family-friends",
        configKey: "familyFriends",
        titleKey: "wizard.users.family.title",
        descriptionKey: "wizard.users.family.description",
        helpKey: "wizard.users.family.help",
        tipKey: "wizard.users.family.tip",
        testId: "easy-users-family",
      },
      {
        key: "public",
        configKey: "public",
        titleKey: "wizard.users.public.title",
        descriptionKey: "wizard.users.public.description",
        helpKey: "wizard.users.public.help",
        tipKey: "wizard.users.public.tip",
        testId: "easy-users-public",
      },
    ],
    vpn: [
      {
        value: "headscale",
        label: "Headscale",
        description: "Tailscale-compatible mesh",
        titleKey: "wizard.access.vpn.headscale",
        descriptionKey: "wizard.access.anywhere.description",
        testId: "techie-vpn-headscale",
      },
      {
        value: "wireguard",
        label: "WireGuard",
        description: "Direct private mesh",
        titleKey: "wizard.access.vpn.wireguard",
        descriptionKey: "wizard.access.anywhere.help",
        testId: "techie-vpn-wireguard",
      },
      {
        value: "none",
        label: "None",
        description: "Local network only",
        titleKey: "wizard.access.home.title",
        descriptionKey: "wizard.access.home.description",
        testId: "techie-vpn-none",
      },
    ],
    // Product invariant: every lane must expose all three server paths.
    // kombify Cloud only changes convenience/defaults; it must never gate
    // one-liner or direct SSH setup for user-owned self-hosted homelabs.
    serverProvisioningModes: [
      {
        value: "install-command",
        titleKey: "wizard.server.oneliner.title",
        descriptionKey: "wizard.server.oneliner.description",
        helpKey: "wizard.server.oneliner.help",
        tipKey: "wizard.server.oneliner.info",
        badgeKey: "common.recommended",
        testId: "server-mode-install-command",
      },
      {
        value: "connect-remote",
        titleKey: "wizard.server.remote.title",
        descriptionKey: "wizard.server.remote.description",
        helpKey: "wizard.server.remote.help",
        testId: "server-mode-connect-remote",
      },
      {
        value: "kombify-cloud",
        titleKey: "wizard.server.cloud.title",
        descriptionKey: "wizard.server.cloud.description",
        helpKey: "wizard.server.cloud.help",
        tipKey: "wizard.server.cloud.info",
        badgeKey: "wizard.server.mode.managed",
        testId: "server-mode-kombify-cloud",
      },
    ],
    ionosDatacenters: [
      {
        value: "de/fra",
        label: "Frankfurt",
        description: "Germany / EU primary",
        testId: "ionos-datacenter-de-fra",
      },
      {
        value: "de/txl",
        label: "Berlin",
        description: "Germany / EU secondary",
        testId: "ionos-datacenter-de-txl",
      },
      {
        value: "us/ewr",
        label: "Newark",
        description: "United States",
        testId: "ionos-datacenter-us-ewr",
      },
    ],
    stackkitFoundations: [
      {
        value: "basement-kit",
        label: "Basement Kit",
        description:
          "Local or user-owned server kit for first rollout and expansion.",
        testId: "foundation-basement-kit",
      },
      {
        value: "cloud-kit",
        label: "Cloud Kit",
        description: "Managed VPS foundation for kombify Cloud rollouts.",
        testId: "foundation-cloud-kit",
      },
    ],
    nodeRoles: [
      {
        value: "foundation",
        label: "Foundation Node",
        description:
          "First/core server; wire-compatible with main, standalone, and control-plane.",
        testId: "server-role-foundation",
      },
      {
        value: "worker",
        label: "Worker Node",
        description:
          "Additional compute server for StackKit service placement.",
        testId: "server-role-worker",
      },
      {
        value: "storage",
        label: "Storage Node",
        description: "Additional server intended for storage-heavy services.",
        testId: "server-role-storage",
      },
    ],
    goalServiceMap: {
      baseline: ["pocketId", "monitoring", "traefik", "vaultwarden"],
      "smart-home": ["monitoring"],
      photos: ["immich"],
      media: ["monitoring"],
      vault: ["vaultwarden"],
      files: ["files"],
      ai: ["monitoring"],
      dev: ["traefik"],
      mail: ["traefik"],
      game: ["traefik"],
      everything: ["monitoring", "traefik", "vaultwarden", "immich", "files"],
      accessAnywhere: ["headscale"],
    },
  },
  stackSpec: {
    coreServices: ["homepage", "whoami", "tinyauth"],
    paasServices: {
      dokploy: "dokploy",
      coolify: "coolify",
    },
  },
  services: [
    {
      key: "pocketId",
      label: "Pocket ID",
      description: "Default identity provider for login-protected services",
      helpText:
        "Pocket ID is the standard external identity head for StackKit access.",
      testId: "techie-svc-pocket-id",
      defaultSelected: true,
      selectableInTechie: false,
      payloadServices: ["pocket-id"],
      stackSpecServices: ["pocketid"],
      stackKitServices: [stackService("pocket-id", "auth")],
    },
    {
      key: "pocketbase",
      label: "PocketBase",
      description: "Auth and database backend",
      helpText:
        "Optional backend capability when a stack needs PocketBase-native auth or data.",
      testId: "techie-svc-pocketbase",
      defaultSelected: false,
      selectableInTechie: true,
      payloadServices: ["pocketbase"],
      stackSpecServices: ["pocketbase"],
      stackKitServices: [stackService("pocketbase", "backend")],
    },
    {
      key: "headscale",
      label: "Headscale",
      description: "Tailscale-compatible mesh VPN",
      helpText:
        "Creates a secure mesh network for device access without public ports.",
      testId: "techie-svc-headscale",
      defaultSelected: false,
      selectableInTechie: true,
      payloadServices: ["headscale"],
      stackSpecServices: ["headscale"],
      stackKitServices: [stackService("headscale", "vpn")],
    },
    {
      key: "traefik",
      label: "Traefik",
      description: "Reverse proxy and load balancer",
      helpText:
        "Handles routing, HTTPS, and service entrypoints for the stack.",
      testId: "techie-svc-traefik",
      defaultSelected: true,
      selectableInTechie: true,
      payloadServices: ["traefik"],
      stackSpecServices: ["traefik"],
      stackKitServices: [stackService("traefik", "reverse-proxy")],
    },
    {
      key: "monitoring",
      label: "OpenTelemetry Collector",
      description: "Standard observability pipeline",
      helpText: "Collects telemetry signals for the Day-2 monitoring baseline.",
      testId: "techie-svc-monitoring",
      defaultSelected: true,
      selectableInTechie: true,
      payloadServices: ["otel-collector"],
      stackSpecServices: ["uptime-kuma"],
      stackKitServices: [stackService("otel-collector", "monitoring")],
    },
    {
      key: "vaultwarden",
      label: "Vaultwarden",
      description: "Password vault",
      helpText:
        "Provides a login-protected password vault as part of the StackKit app baseline.",
      testId: "techie-svc-vaultwarden",
      defaultSelected: true,
      selectableInTechie: true,
      payloadServices: ["vaultwarden"],
      stackSpecServices: ["vaultwarden"],
      stackKitServices: [stackService("vaultwarden", "auth")],
    },
    {
      key: "immich",
      label: "Immich",
      description:
        "Photo library with supporting database, cache, and ML services",
      helpText:
        "Expands to the Immich server, machine-learning worker, Postgres, and Redis specs.",
      testId: "techie-svc-immich",
      defaultSelected: true,
      selectableInTechie: true,
      payloadServices: [
        "immich-server",
        "immich-ml",
        "immich-postgres",
        "immich-redis",
      ],
      stackSpecServices: ["immich"],
      stackKitServices: [
        stackService("immich-postgres", "database"),
        stackService("immich-redis", "cache"),
        stackService("immich-ml", "media"),
        stackService("immich-server", "media", [
          "immich-postgres",
          "immich-redis",
        ]),
      ],
    },
    {
      key: "files",
      label: "Files",
      description: "File storage module",
      helpText:
        "Reserved for the file-storage module once it is in the release baseline.",
      testId: "techie-svc-files",
      defaultSelected: false,
      selectableInTechie: true,
      payloadServices: ["files"],
      stackSpecServices: ["files"],
      stackKitServices: [stackService("files", "storage")],
    },
  ],
};

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function addUnique<T>(
  items: T[],
  candidate: T,
  key: (item: T) => string,
): void {
  if (items.some((item) => key(item) === key(candidate))) return;
  items.push(candidate);
}

export function cloneStandardBundleDefaults() {
  return clone(ACTIVE_STANDARD_BUNDLE.defaults);
}

export function cloneDefaultServerProvisioning() {
  return clone(ACTIVE_STANDARD_BUNDLE.defaults.serverProvisioning);
}

export function serviceConfigKeysFromBundle(): StandardBundleServiceKey[] {
  return ACTIVE_STANDARD_BUNDLE.services.map((service) => service.key);
}

export function getTechieServiceQuestions(): BundleServiceDefinition[] {
  return ACTIVE_STANDARD_BUNDLE.services.filter(
    (service) => service.selectableInTechie,
  );
}

export function getEasyGoalQuestions(): BundleQuestionDefinition<GoalConfigKey>[] {
  return ACTIVE_STANDARD_BUNDLE.wizard.goals;
}

export function getEasyAccessQuestions(): BundleChoiceDefinition<AccessModeValue>[] {
  return ACTIVE_STANDARD_BUNDLE.wizard.access;
}

export function getEasyAudienceQuestions(): BundleQuestionDefinition<AudienceConfigKey>[] {
  return ACTIVE_STANDARD_BUNDLE.wizard.audience;
}

export function getVpnQuestions(): BundleChoiceDefinition<VpnValue>[] {
  return ACTIVE_STANDARD_BUNDLE.wizard.vpn;
}

export function getServerProvisioningModeChoices(): BundleChoiceDefinition<ServerProvisioningModeValue>[] {
  return ACTIVE_STANDARD_BUNDLE.wizard.serverProvisioningModes;
}

export function getIonosDatacenterChoices(): BundleRegistryChoiceDefinition<IonosDatacenterValue>[] {
  return ACTIVE_STANDARD_BUNDLE.wizard.ionosDatacenters;
}

export function getStackKitFoundationChoices(): BundleRegistryChoiceDefinition<StackKitFoundationValue>[] {
  return ACTIVE_STANDARD_BUNDLE.wizard.stackkitFoundations;
}

export function getRegistryNodeRoleChoices(): BundleRegistryChoiceDefinition<RegistryNodeRoleValue>[] {
  return ACTIVE_STANDARD_BUNDLE.wizard.nodeRoles;
}

export function isDiscoveryEnabledForWizard(surface: WizardSurface): boolean {
  return ACTIVE_STANDARD_BUNDLE.wizard.discovery[surface] === true;
}

export function buildPayloadServicesFromBundle(
  serviceFlags: StandardBundleServiceSelections,
): string[] {
  const services: string[] = [];
  for (const definition of ACTIVE_STANDARD_BUNDLE.services) {
    if (!serviceFlags[definition.key]) continue;
    for (const name of definition.payloadServices) {
      if (!services.includes(name)) services.push(name);
    }
  }
  return services;
}

export function buildStackSpecServiceTogglesFromBundle(
  serviceFlags: StandardBundleServiceSelections,
): Record<string, boolean> {
  const toggles: Record<string, boolean> = {};
  for (const service of ACTIVE_STANDARD_BUNDLE.stackSpec.coreServices) {
    toggles[service] = true;
  }
  for (const definition of ACTIVE_STANDARD_BUNDLE.services) {
    for (const name of definition.stackSpecServices) {
      toggles[name] = serviceFlags[definition.key] === true;
    }
  }
  return toggles;
}

export function buildStackKitServicesFromBundle(
  serviceFlags: StandardBundleServiceSelections,
): Array<StackKitServiceDefinition & { node: string }> {
  const services: Array<StackKitServiceDefinition & { node: string }> = [];
  for (const definition of ACTIVE_STANDARD_BUNDLE.services) {
    if (!serviceFlags[definition.key]) continue;
    for (const spec of definition.stackKitServices) {
      addUnique(
        services,
        {
          ...spec,
          node: mainNode,
        },
        (service) => service.name,
      );
    }
  }
  return services;
}

export function applyStandardBundleGoalServices(
  serviceFlags: Record<StandardBundleServiceKey, boolean>,
  goals: Partial<Record<GoalConfigKey, boolean>> | undefined,
  accessMode: AccessModeValue,
): void {
  for (const key of ACTIVE_STANDARD_BUNDLE.wizard.goalServiceMap.baseline) {
    serviceFlags[key] = true;
  }
  if (goals) {
    for (const goal of ACTIVE_STANDARD_BUNDLE.wizard.goals) {
      if (!goals[goal.configKey]) continue;
      for (const key of ACTIVE_STANDARD_BUNDLE.wizard.goalServiceMap[
        goal.configKey
      ]) {
        serviceFlags[key] = true;
      }
    }
  }
  serviceFlags.headscale = accessMode === "anywhere";
}

export function selectedCanonicalUseCasesFromGoals(
  goals: Partial<Record<GoalConfigKey, boolean>> | undefined,
): CanonicalUseCaseGoalKey[] {
  if (!goals) return [];
  if (goals.everything) return [...CANONICAL_USE_CASE_GOALS];
  return CANONICAL_USE_CASE_GOALS.filter((goal) => goals[goal] === true);
}
