/**
 * Wallet Integration Service
 * Handles auto-population and service discovery for credentials
 */

import {
  getWalletItems,
  getWalletItemsByStack,
  createWalletItem,
} from "$lib/api/wallet";
import { listServiceRegistry, type RegistryService } from "$lib/api/registry";
import type { PBService } from "$lib/stores/services";
import type { PBStack } from "$lib/stores/stacks";
import type { CredentialType } from "$lib/stores/wallet";
import { buildWalletEntryPayload } from "$lib/wallet/payload";

export interface DiscoveredCredential {
  name: string;
  kind: CredentialType;
  username?: string;
  url?: string;
  notes?: string;
  service_id?: string;
  stack_id?: string;
  auto_generated: boolean;
}

export interface ServiceCredentialTemplate {
  serviceType: string;
  credentials: Array<{
    nameSuffix: string;
    kind: CredentialType;
    usernameDefault?: string;
    urlPattern?: string;
    notes?: string;
  }>;
}

/**
 * Service credential templates - defines what credentials each service type typically needs
 */
const SERVICE_TEMPLATES: ServiceCredentialTemplate[] = [
  {
    serviceType: "pocketbase",
    credentials: [
      {
        nameSuffix: "Admin",
        kind: "password",
        usernameDefault: "admin@local.host",
        urlPattern: "{url}/_/",
        notes: "PocketBase admin dashboard credentials",
      },
    ],
  },
  {
    serviceType: "traefik",
    credentials: [
      {
        nameSuffix: "Dashboard",
        kind: "password",
        usernameDefault: "admin",
        urlPattern: "{url}/dashboard/",
        notes: "Traefik dashboard basic auth",
      },
    ],
  },
  {
    serviceType: "headscale",
    credentials: [
      {
        nameSuffix: "API Key",
        kind: "api_key",
        urlPattern: "{url}/api/v1",
        notes: "Headscale API key for management",
      },
    ],
  },
  {
    serviceType: "monitoring",
    credentials: [
      {
        nameSuffix: "Grafana",
        kind: "password",
        usernameDefault: "admin",
        urlPattern: "{url}",
        notes: "Grafana dashboard credentials",
      },
    ],
  },
];

/**
 * Discover credentials that should be added for a service
 */
export function discoverServiceCredentials(
  service: PBService,
  stack?: PBStack,
): DiscoveredCredential[] {
  const template = SERVICE_TEMPLATES.find(
    (t) => t.serviceType === service.type,
  );
  if (!template) return [];

  return template.credentials.map((cred) => ({
    name: `${service.display_name || service.name} ${cred.nameSuffix}`,
    kind: cred.kind,
    username: cred.usernameDefault,
    url: cred.urlPattern?.replace("{url}", service.url || ""),
    notes: cred.notes,
    service_id: service.id,
    stack_id: stack?.id,
    auto_generated: false, // User needs to fill in the secret
  }));
}

/**
 * Get all services that don't have corresponding wallet entries
 */
export async function findServicesWithoutCredentials(): Promise<
  Array<{
    service: PBService;
    missingCredentials: DiscoveredCredential[];
  }>
> {
  const [{ services }, wallet] = await Promise.all([
    listServiceRegistry(),
    getWalletItems(),
  ]);

  const serviceIdsWithCredentials = new Set(
    wallet.filter((w) => w.service_id).map((w) => w.service_id),
  );

  const results: Array<{
    service: PBService;
    missingCredentials: DiscoveredCredential[];
  }> = [];

  for (const registryService of services) {
    const service = registryServiceToPBService(registryService);
    if (serviceIdsWithCredentials.has(service.id)) continue;

    const discovered = discoverServiceCredentials(service);
    if (discovered.length > 0) {
      results.push({
        service,
        missingCredentials: discovered,
      });
    }
  }

  return results;
}

function registryServiceToPBService(service: RegistryService): PBService {
  return {
    id: service.id || service.name,
    name: service.name,
    display_name: service.display_name,
    type: service.type as PBService["type"],
    port: service.port,
    url: service.url,
    status: service.status as PBService["status"],
    node_id: service.server_id,
  } as PBService;
}

/**
 * Create placeholder credentials for a stack after provisioning
 */
export async function createStackCredentials(
  stackId: string,
  stackName: string,
): Promise<void> {
  // Create SSH key placeholder
  await createWalletItem(
    buildWalletEntryPayload("recovery", {
      name: `${stackName} SSH Key`,
      kind: "ssh_key",
      notes: `Auto-generated SSH key for stack "${stackName}". Generate or add your key here.`,
      stack_id: stackId,
      auto_generated: true,
    }),
  );

  // Create root password placeholder
  await createWalletItem(
    buildWalletEntryPayload("recovery", {
      name: `${stackName} Root Password`,
      kind: "password",
      username: "root",
      notes: `Root password for nodes in stack "${stackName}".`,
      stack_id: stackId,
      auto_generated: true,
    }),
  );
}

/**
 * Add credentials for a discovered service
 */
export async function addServiceCredentials(
  discovered: DiscoveredCredential,
): Promise<void> {
  await createWalletItem(
    buildWalletEntryPayload("recovery", {
      name: discovered.name,
      kind: discovered.kind,
      username: discovered.username,
      url: discovered.url,
      notes: discovered.notes,
      service_id: discovered.service_id,
      stack_id: discovered.stack_id,
      auto_generated: discovered.auto_generated,
    }),
  );
}

/**
 * Get suggested rotation interval for a credential type
 */
export function getRotationInterval(kind: CredentialType): number | null {
  switch (kind) {
    case "api_key":
      return 90; // 90 days
    case "password":
      return 180; // 180 days
    case "oauth_token":
      return 30; // 30 days
    case "certificate":
      return 365; // 1 year
    default:
      return null;
  }
}

/**
 * Check if a credential should be rotated
 */
export function shouldRotate(
  lastRotated: string | undefined,
  kind: CredentialType,
): boolean {
  const interval = getRotationInterval(kind);
  if (!interval || !lastRotated) return false;

  const lastDate = new Date(lastRotated);
  const now = new Date();
  const daysSinceRotation = Math.floor(
    (now.getTime() - lastDate.getTime()) / (1000 * 60 * 60 * 24),
  );

  return daysSinceRotation >= interval;
}

/**
 * Get days until rotation is recommended
 */
export function getDaysUntilRotation(
  lastRotated: string | undefined,
  kind: CredentialType,
): number | null {
  const interval = getRotationInterval(kind);
  if (!interval || !lastRotated) return null;

  const lastDate = new Date(lastRotated);
  const now = new Date();
  const daysSinceRotation = Math.floor(
    (now.getTime() - lastDate.getTime()) / (1000 * 60 * 60 * 24),
  );

  return Math.max(0, interval - daysSinceRotation);
}
