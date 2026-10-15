import { tr } from "#lib/i18n.svelte.js";
/** Techstack wizard questions and intent preferences. StackKits authoring and service composition belong to the pinned release CLI. */

/**
 * The wizard has one surface. Technical depth is a property of the use case
 * card (compact -> large -> advanced), not a separate wizard.
 */
export type WizardSurface = "easy";

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
  "marketplace",
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
  "marketplace",
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
} as const satisfies Partial<Record<CanonicalUseCaseGoalKey, string>>;
export type AudienceConfigKey = "onlyMe" | "familyFriends" | "public";
export type AccessModeValue = "home" | "anywhere";
export type VpnValue = "headscale" | "wireguard" | "none";
export type StackKitFoundationValue = "basement-kit" | "cloud-kit";
export type RegistryNodeRoleValue = "foundation" | "worker" | "storage";
export type ServerProvisioningModeValue =
  "kombify-cloud" | "connect-remote" | "install-command" | "hypervisor";
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

export interface BundleServiceDefinition {
  key: StandardBundleServiceKey;
  label: string;
  description: string;
  helpText: string;
  testId: string;
  defaultSelected: boolean;
  selectableInTechie: boolean;
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
        authMethod: "ssh-key" | "password";
        sshKeyLabel: string;
        sshPassword: string;
        sshWalletItemId?: string;
        useSudo: boolean;
      };
    };
    goals: Record<GoalConfigKey, boolean>;
    services: Record<StandardBundleServiceKey, boolean>;
    network: {
      accessMode: AccessModeValue;
      vpn: VpnValue;
      enableCloudflare: boolean;
      publicAccess: boolean;
      reverseProxy: boolean;
    };
    audience: Record<AudienceConfigKey, boolean>;
    identity: {
      toolProvider: "pocket-id";
      homelabProvider: "pocket-id";
      requiresPasskeys: boolean;
      backendCapability: "passkeys";
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
  };
  services: BundleServiceDefinition[];
}

export const USER_OWNED_STACKKIT_REF = "basement-kit";
export const CLOUD_STACKKIT_REF = "cloud-kit";

export function normalizeInstallableKitSlug(
  raw: string,
  managedLane: boolean,
): string {
  const trimmed = raw.trim();
  if (trimmed === "") {
    return managedLane ? CLOUD_STACKKIT_REF : USER_OWNED_STACKKIT_REF;
  }
  return trimmed;
}

export const ACTIVE_STANDARD_BUNDLE: StandardBundleDefinition = {
  id: "basement-kit.standard.v1",
  version: "1.0.0",
  kit: "basement-kit",
  get releaseLabel() {
    return tr("ui.standardBundle.basementKitStandardRelease");
  },
  defaults: {
    name: "homelab",
    provider: "local",
    wizardType: "easy",
    kit: "basement-kit",
    serverMode: "user-owned",
    runtimeOfferingId: "monthly-runtime-standard",
    providerId: "ionos",
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
        sshPassword: "",
        useSudo: false,
      },
    },
    goals: {
      "smart-home": false,
      photos: false,
      media: false,
      vault: false,
      files: false,
      ai: false,
      dev: false,
      mail: false,
      game: false,
      marketplace: false,
      everything: false,
    },
    services: {
      pocketId: true,
      pocketbase: false,
      headscale: false,
      monitoring: true,
      traefik: true,
      vaultwarden: false,
      immich: false,
      files: false,
    },
    network: {
      accessMode: "home",
      vpn: "none",
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
      requiresPasskeys: true,
      backendCapability: "passkeys",
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
        {
          id: 1,
          key: "goals",
          get label() {
            return tr("ui.standardBundle.goals");
          },
          labelKey: "wizard.step.goals",
        },
        {
          id: 2,
          key: "server",
          get label() {
            return tr("ui.services.node");
          },
          labelKey: "wizard.step.server",
        },
        {
          id: 3,
          key: "access",
          get label() {
            return tr("ui.stacksCreatingCreationLease.access");
          },
          labelKey: "wizard.step.access",
        },
        {
          id: 4,
          key: "users",
          get label() {
            return tr("ui.standardBundle.users");
          },
          labelKey: "wizard.step.users",
        },
        {
          id: 5,
          key: "login",
          get label() {
            return tr("ui.standardBundle.login");
          },
          labelKey: "wizard.step.login",
        },
      ],
    },
    discovery: {
      easy: false,
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
      {
        key: "marketplace",
        configKey: "marketplace",
        surface: "advanced",
        titleKey: "wizard.goals.marketplace.title",
        descriptionKey: "wizard.goals.marketplace.description",
        helpKey: "wizard.goals.marketplace.help",
        tipKey: "wizard.goals.marketplace.tip",
        testId: "easy-feature-marketplace",
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
        get description() {
          return tr("ui.standardBundle.directPrivateMesh");
        },
        titleKey: "wizard.access.vpn.wireguard",
        descriptionKey: "wizard.access.anywhere.help",
        testId: "techie-vpn-wireguard",
      },
      {
        value: "none",
        get label() {
          return tr("ui.standardBundle.none");
        },
        get description() {
          return tr("ui.standardBundle.localNetworkOnly");
        },
        titleKey: "wizard.access.home.title",
        descriptionKey: "wizard.access.home.description",
        testId: "techie-vpn-none",
      },
    ],
    // Product invariant: every lane exposes the same provisioning choices.
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
        get description() {
          return tr("ui.standardBundle.germanyEUPrimary");
        },
        testId: "ionos-datacenter-de-fra",
      },
      {
        value: "de/txl",
        label: "Berlin",
        get description() {
          return tr("ui.standardBundle.germanyEUSecondary");
        },
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
        get description() {
          return tr("ui.standardBundle.localOrUserOwnedNode");
        },
        testId: "foundation-basement-kit",
      },
      {
        value: "cloud-kit",
        label: "Cloud Kit",
        get description() {
          return tr("ui.standardBundle.managedVPSFoundationForKombify");
        },
        testId: "foundation-cloud-kit",
      },
    ],
    nodeRoles: [
      {
        value: "foundation",
        get label() {
          return tr("ui.standardBundle.foundationNode");
        },
        get description() {
          return tr("ui.standardBundle.firstCoreNodeWireCompatible");
        },
        testId: "server-role-foundation",
      },
      {
        value: "worker",
        get label() {
          return tr("ui.standardBundle.workerNode");
        },
        get description() {
          return tr("ui.standardBundle.additionalComputeNodeForStackKit");
        },
        testId: "server-role-worker",
      },
      {
        value: "storage",
        get label() {
          return tr("ui.standardBundle.storageNode");
        },
        get description() {
          return tr("ui.standardBundle.additionalNodeIntendedForStorage");
        },
        testId: "server-role-storage",
      },
    ],
  },
  services: [
    {
      key: "pocketId",
      label: "Pocket ID",
      get description() {
        return tr("ui.standardBundle.defaultIdentityProviderForLogin");
      },
      get helpText() {
        return tr("ui.standardBundle.pocketIDIsTheStandard");
      },
      testId: "techie-svc-pocket-id",
      defaultSelected: true,
      selectableInTechie: false,
    },
    {
      key: "pocketbase",
      label: "PocketBase",
      get description() {
        return tr("ui.standardBundle.authAndDatabaseBackend");
      },
      get helpText() {
        return tr("ui.standardBundle.optionalBackendCapabilityWhenA");
      },
      testId: "techie-svc-pocketbase",
      defaultSelected: false,
      selectableInTechie: true,
    },
    {
      key: "headscale",
      label: "Headscale",
      get description() {
        return tr("ui.standardBundle.tailscaleCompatibleMeshVPN");
      },
      get helpText() {
        return tr("ui.standardBundle.createsASecureMeshNetwork");
      },
      testId: "techie-svc-headscale",
      defaultSelected: false,
      selectableInTechie: true,
    },
    {
      key: "traefik",
      label: "Traefik",
      get description() {
        return tr("ui.standardBundle.reverseProxyAndLoadBalancer");
      },
      get helpText() {
        return tr("ui.standardBundle.handlesRoutingHTTPSAndService");
      },
      testId: "techie-svc-traefik",
      defaultSelected: true,
      selectableInTechie: true,
    },
    {
      key: "monitoring",
      label: "OpenTelemetry Collector",
      get description() {
        return tr("ui.standardBundle.standardObservabilityPipeline");
      },
      get helpText() {
        return tr("ui.standardBundle.collectsTelemetrySignalsForThe");
      },
      testId: "techie-svc-monitoring",
      defaultSelected: true,
      selectableInTechie: true,
    },
    {
      key: "vaultwarden",
      label: "Vaultwarden",
      get description() {
        return tr("ui.standardBundle.passwordVault");
      },
      get helpText() {
        return tr("ui.standardBundle.addsALoginProtectedPassword");
      },
      testId: "techie-svc-vaultwarden",
      defaultSelected: false,
      selectableInTechie: true,
    },
    {
      key: "immich",
      label: "Immich",
      get description() {
        return tr("ui.standardBundle.photoLibraryWithSupportingDatabase");
      },
      get helpText() {
        return tr("ui.standardBundle.expandsToTheImmichServer");
      },
      testId: "techie-svc-immich",
      defaultSelected: false,
      selectableInTechie: true,
    },
    {
      key: "files",
      get label() {
        return tr("ui.standardBundle.files");
      },
      get description() {
        return tr("ui.standardBundle.fileStorageModule");
      },
      get helpText() {
        return tr("ui.standardBundle.reservedForTheFileStorage");
      },
      testId: "techie-svc-files",
      defaultSelected: false,
      selectableInTechie: true,
    },
  ],
};

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
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

export function selectedCanonicalUseCasesFromGoals(
  goals: Partial<Record<GoalConfigKey, boolean>> | undefined,
): CanonicalUseCaseGoalKey[] {
  if (!goals) return [];
  // Templates toggle available cards. The marker must not reintroduce
  // goals absent from the release or left out by recommendations.
  return CANONICAL_USE_CASE_GOALS.filter((goal) => goals[goal] === true);
}
