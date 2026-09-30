import { tr } from "#lib/i18n.svelte.js";
/**
 * kombify-TechStack Deployment Requirements
 *
 * Derives server/resource requirements from a stack configuration and renders
 * the worker installation commands. Split out of tasks.ts so the requirements
 * model and the Unifier RequirementsSpec mapping live together.
 */

/**
 * Deployment requirements based on stack configuration
 * Extended to match the full RequirementsSpec from the backend Unifier
 */
export interface DeploymentRequirements {
  // Basic counts
  minCloudServers: number;
  minLocalServers: number;
  minTotalServers: number;

  // Description (legacy and new)
  description: string;
  details?: string[];

  // Extended fields from Unifier.Analyze()
  stackKit?: string;
  detectedAddons?: string[];
  minRAM?: number;
  minCPU?: number;
  specialRequirements?: string[];
  requiredCredentials?: Array<{
    key: string;
    label: string;
    description: string;
    required: boolean;
    type: string;
    helpUrl?: string;
  }>;
  requiredPreChecks?: Array<{
    type: string;
    description: string;
    minVersion?: string;
    blocking: boolean;
  }>;
  appliedDefaults?: Record<string, unknown>;
}

/**
 * Calculate deployment requirements based on stack configuration
 */
export function calculateRequirements(config: {
  provider?: string;
  serverProvisioning?: {
    mode?:
      "kombify-cloud" | "connect-remote" | "install-command" | "hypervisor";
  };
  network?: { accessMode?: string };
  goals?: Partial<
    Record<
      | "smart-home"
      | "photos"
      | "media"
      | "vault"
      | "files"
      | "ai"
      | "dev"
      | "mail"
      | "game"
      | "everything",
      boolean
    >
  >;
  services?: Record<string, boolean>;
}): DeploymentRequirements {
  let minCloudServers = 0;
  let minLocalServers = 0;
  const details: string[] = [];

  const serverProvisioningMode = config.serverProvisioning?.mode;

  if (serverProvisioningMode === "hypervisor") {
    return {
      minCloudServers: 0,
      minLocalServers: 1,
      minTotalServers: 1,
      description: tr("ui.requirements.ubuntuGuestOnYourProxmox"),
      details: [
        tr("ui.requirements.techstackPreparesTheVMThen"),
        ...(config.goals?.["smart-home"]
          ? [tr("ui.requirements.homeAssistantOSCanRun")]
          : []),
      ],
    };
  }

  if (serverProvisioningMode === "kombify-cloud") {
    minCloudServers = 1;
    details.push(tr("ui.requirements.aKombifyCloudServerIs"));
    if (config.goals?.everything) {
      details.push(tr("ui.requirements.completeSetupKombifyCloudServer"));
    }

    return {
      minCloudServers,
      minLocalServers,
      minTotalServers: minCloudServers + minLocalServers,
      description: tr("ui.requirements.1KombifyCloudServerIs"),
      details,
    };
  }

  if (serverProvisioningMode === "connect-remote") {
    minLocalServers = 1;
    details.push(tr("ui.requirements.anExistingRemoteServerIs"));
    if (config.goals?.everything) {
      details.push(tr("ui.requirements.completeSetupAtLeast1"));
    }

    return {
      minCloudServers,
      minLocalServers,
      minTotalServers: minCloudServers + minLocalServers,
      description: tr("ui.requirements.atLeast1ExistingServer"),
      details,
    };
  }

  if (serverProvisioningMode === "install-command") {
    minLocalServers = 1;
    details.push(tr("ui.requirements.theInstallationCommandRunsOn"));
    if (config.goals?.everything) {
      details.push(tr("ui.requirements.completeSetupAtLeast1"));
    }

    return {
      minCloudServers,
      minLocalServers,
      minTotalServers: minCloudServers + minLocalServers,
      description: tr("ui.requirements.atLeast1UserOwned"),
      details,
    };
  }

  // Determine server requirements based on provider/access mode
  const accessMode = config.network?.accessMode || "local";
  const provider = config.provider || "local";

  if (provider === "cloud" || accessMode === "anywhere") {
    minCloudServers = 1;
    details.push(tr("ui.requirements.aCloudServerIsRequired"));
  }

  if (
    provider === "local" ||
    provider === "hybrid" ||
    accessMode !== "cloud-only"
  ) {
    minLocalServers = 1;
    details.push(tr("ui.requirements.aLocalServerIsRequired"));
  }

  // Hybrid setup needs both
  if (provider === "hybrid") {
    minCloudServers = Math.max(minCloudServers, 1);
    minLocalServers = Math.max(minLocalServers, 1);
    details.push(tr("ui.requirements.hybridSetupCloudAndLocal"));
  }

  if (config.goals?.everything) {
    minLocalServers = Math.max(minLocalServers, 1);
    details.push(tr("ui.requirements.completeSetupAtLeast1"));
  }

  const minTotalServers = minCloudServers + minLocalServers;

  // Build description
  let description = "";
  if (minCloudServers > 0 && minLocalServers > 0) {
    description = tr("ui.requirements.atLeastCloudServerAnd", {
      minCloudServers,
      minLocalServers,
    });
  } else if (minCloudServers > 0) {
    description = tr("ui.requirements.atLeastCloudServer", { minCloudServers });
  } else {
    description = tr("ui.requirements.atLeastLocalServer", { minLocalServers });
  }

  return {
    minCloudServers,
    minLocalServers,
    minTotalServers,
    description,
    details,
  };
}

/**
 * Generate worker installation command
 */
export function generateInstallCommand(
  serverUrl: string,
  registrationToken: string,
): string {
  const baseUrl = normalizeWorkerInstallBaseUrl(serverUrl);
  return `curl -fsSL ${baseUrl}/install.sh | KOMBI_SERVER="${baseUrl}" KOMBI_TOKEN="${registrationToken}" TECHSTACK_AS_SERVICE=1 bash`;
}

export function normalizeWorkerInstallBaseUrl(serverUrl: string): string {
  const trimmed = serverUrl.replace(/\/$/, "");
  try {
    const url = new URL(trimmed);
    // In local dev, the UI runs on :5261 but the API (and /install.sh) is served by the core on :5260.
    if (url.port === "5261") url.port = "5260";
    return url.toString().replace(/\/$/, "");
  } catch {
    // Best-effort fallback for non-URL strings.
    return trimmed.replace(":5261", ":5260");
  }
}

/**
 * Generate alternative installation commands for different platforms
 */
export function generateInstallCommands(
  serverUrl: string,
  registrationToken: string,
): { platform: string; command: string; description: string }[] {
  const baseUrl = normalizeWorkerInstallBaseUrl(serverUrl);

  return [
    {
      platform: "linux",
      command: `curl -fsSL ${baseUrl}/install.sh | KOMBI_SERVER="${baseUrl}" KOMBI_TOKEN="${registrationToken}" TECHSTACK_AS_SERVICE=1 bash`,
      description: tr("ui.requirements.linuxRecommendedInstallsThePersistent"),
    },
    {
      platform: "docker",
      command: `docker run -d --name techstack-agent -e KOMBI_SERVER="${baseUrl}" -e KOMBI_TOKEN="${registrationToken}" techstack/agent:latest`,
      description: tr("ui.requirements.dockerContainer"),
    },
    {
      platform: "manual",
      command: `techstack agent register --server "${baseUrl}" --token "${registrationToken}"`,
      description: tr("ui.requirements.manualInstallationAfterBinaryDownload"),
    },
  ];
}
