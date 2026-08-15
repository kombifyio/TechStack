/**
 * StackKit Service Definitions
 *
 * Pre-configured services available in StackKits.
 * Used by the Wizard to offer service selection.
 */

import type { ServiceDefinition, ServiceType } from "./schema";

// =============================================================================
// SERVICE CATALOG
// =============================================================================

export interface ServiceCatalogEntry {
  /** Service identifier (DNS-compatible) */
  id: string;

  /** Display name for UI */
  displayName: string;

  /** Service category */
  type: ServiceType;

  /** Short description */
  description: string;

  /** Container image */
  image: string;

  /** Recommended image tag */
  tag: string;

  /** Service dependencies */
  needs: string[];

  /** Default ports */
  ports: { container: number; host?: number; protocol?: "tcp" | "udp" }[];

  /** Requires reverse proxy (Traefik) */
  requiresProxy: boolean;

  /** Has admin UI */
  hasAdminUI: boolean;

  /** Documentation URL */
  docsUrl?: string;

  /** Icon (for UI) */
  icon?: string;

  /** Available in which StackKits */
  availableIn: string[];

  /** Is this a required service for its StackKits */
  required?: boolean;

  /** Is this a recommended service for its StackKits */
  recommended?: boolean;
}

// =============================================================================
// BASEMENT/CLOUD KIT SERVICES
// =============================================================================

export const TRAEFIK: ServiceCatalogEntry = {
  id: "traefik",
  displayName: "Traefik",
  type: "reverse-proxy",
  description: "Modern reverse proxy and load balancer with auto-SSL",
  image: "traefik",
  tag: "v3.0",
  needs: [],
  ports: [
    { container: 80, host: 80 },
    { container: 443, host: 443 },
    { container: 8080 }, // Dashboard
  ],
  requiresProxy: false,
  hasAdminUI: true,
  docsUrl: "https://doc.traefik.io/traefik/",
  icon: "🚦",
  availableIn: ["basement-kit", "cloud-kit"],
  required: true,
};

export const POCKET_ID: ServiceCatalogEntry = {
  id: "pocket-id",
  displayName: "Pocket ID",
  type: "auth",
  description: "Passwordless identity provider for the StackKit login gateway",
  image: "ghcr.io/pocket-id/pocket-id",
  tag: "latest",
  needs: ["traefik"],
  ports: [{ container: 1411 }],
  requiresProxy: true,
  hasAdminUI: true,
  docsUrl: "https://pocket-id.org/",
  availableIn: ["basement-kit", "cloud-kit"],
  required: true,
};

export const VAULTWARDEN: ServiceCatalogEntry = {
  id: "vaultwarden",
  displayName: "Vaultwarden",
  type: "auth",
  description: "Self-hosted password vault protected by the StackKit gateway",
  image: "vaultwarden/server",
  tag: "1.32.5",
  needs: ["traefik", "pocket-id"],
  ports: [{ container: 80 }],
  requiresProxy: true,
  hasAdminUI: true,
  docsUrl: "https://github.com/dani-garcia/vaultwarden",
  availableIn: ["basement-kit", "cloud-kit"],
  recommended: true,
};

export const IMMICH_SERVER: ServiceCatalogEntry = {
  id: "immich-server",
  displayName: "Immich",
  type: "media",
  description:
    "Self-hosted photo and video library protected by the StackKit gateway",
  image: "ghcr.io/immich-app/immich-server",
  tag: "v1.124.2",
  needs: ["traefik", "pocket-id", "immich-postgres", "immich-redis"],
  ports: [{ container: 2283 }],
  requiresProxy: true,
  hasAdminUI: true,
  docsUrl: "https://immich.app/docs/",
  availableIn: ["basement-kit", "cloud-kit"],
  recommended: true,
};

export const IMMICH_ML: ServiceCatalogEntry = {
  id: "immich-ml",
  displayName: "Immich Machine Learning",
  type: "media",
  description: "Immich machine-learning sidecar for search and recognition",
  image: "ghcr.io/immich-app/immich-machine-learning",
  tag: "v1.124.2",
  needs: ["immich-server"],
  ports: [{ container: 3003 }],
  requiresProxy: false,
  hasAdminUI: false,
  docsUrl: "https://immich.app/docs/features/ml-hardware-acceleration",
  availableIn: ["basement-kit", "cloud-kit"],
  recommended: true,
};

export const IMMICH_POSTGRES: ServiceCatalogEntry = {
  id: "immich-postgres",
  displayName: "Immich PostgreSQL",
  type: "database",
  description: "Database backing the verified Immich Basement Kit path",
  image: "tensorchord/pgvecto-rs",
  tag: "pg14-v0.2.0",
  needs: [],
  ports: [{ container: 5432 }],
  requiresProxy: false,
  hasAdminUI: false,
  availableIn: ["basement-kit", "cloud-kit"],
  recommended: true,
};

export const IMMICH_REDIS: ServiceCatalogEntry = {
  id: "immich-redis",
  displayName: "Immich Redis",
  type: "cache",
  description: "Cache backing the verified Immich Basement Kit path",
  image: "redis",
  tag: "7-alpine",
  needs: [],
  ports: [{ container: 6379 }],
  requiresProxy: false,
  hasAdminUI: false,
  docsUrl: "https://redis.io/docs/",
  availableIn: ["basement-kit", "cloud-kit"],
  recommended: true,
};

export const PORTAINER: ServiceCatalogEntry = {
  id: "portainer",
  displayName: "Portainer",
  type: "monitoring",
  description: "Container management UI for Docker",
  image: "portainer/portainer-ce",
  tag: "latest",
  needs: ["traefik"],
  ports: [{ container: 9000 }],
  requiresProxy: true,
  hasAdminUI: true,
  docsUrl: "https://docs.portainer.io/",
  icon: "🐳",
  availableIn: ["basement-kit", "cloud-kit"],
  recommended: true,
};

export const WATCHTOWER: ServiceCatalogEntry = {
  id: "watchtower",
  displayName: "Watchtower",
  type: "automation",
  description: "Automatic container image updates",
  image: "containrrr/watchtower",
  tag: "latest",
  needs: [],
  ports: [],
  requiresProxy: false,
  hasAdminUI: false,
  docsUrl: "https://containrrr.dev/watchtower/",
  icon: "🗼",
  availableIn: ["basement-kit", "cloud-kit"],
  recommended: true,
};

export const HOMEPAGE: ServiceCatalogEntry = {
  id: "homepage",
  displayName: "Homepage",
  type: "custom",
  description: "Modern dashboard for your homelab",
  image: "ghcr.io/gethomepage/homepage",
  tag: "latest",
  needs: ["traefik"],
  ports: [{ container: 3000 }],
  requiresProxy: true,
  hasAdminUI: true,
  docsUrl: "https://gethomepage.dev/",
  icon: "🏠",
  availableIn: ["basement-kit", "cloud-kit"],
};

export const UPTIME_KUMA: ServiceCatalogEntry = {
  id: "uptime-kuma",
  displayName: "Uptime Kuma",
  type: "monitoring",
  description: "Self-hosted uptime monitoring tool",
  image: "louislam/uptime-kuma",
  tag: "1",
  needs: ["traefik"],
  ports: [{ container: 3001 }],
  requiresProxy: true,
  hasAdminUI: true,
  docsUrl: "https://github.com/louislam/uptime-kuma",
  icon: "📈",
  availableIn: ["basement-kit", "cloud-kit"],
};

export const DOZZLE: ServiceCatalogEntry = {
  id: "dozzle",
  displayName: "Dozzle",
  type: "monitoring",
  description: "Real-time Docker log viewer",
  image: "amir20/dozzle",
  tag: "latest",
  needs: ["traefik"],
  ports: [{ container: 8080 }],
  requiresProxy: true,
  hasAdminUI: true,
  docsUrl: "https://dozzle.dev/",
  icon: "📋",
  availableIn: ["basement-kit", "cloud-kit"],
};

// =============================================================================
// DATABASE SERVICES (for advanced kits)
// =============================================================================

export const POSTGRESQL: ServiceCatalogEntry = {
  id: "postgresql",
  displayName: "PostgreSQL",
  type: "database",
  description: "Powerful, open-source relational database",
  image: "postgres",
  tag: "16-alpine",
  needs: [],
  ports: [{ container: 5432 }],
  requiresProxy: false,
  hasAdminUI: false,
  docsUrl: "https://www.postgresql.org/docs/",
  icon: "🐘",
  availableIn: ["cloud-kit"],
};

export const REDIS: ServiceCatalogEntry = {
  id: "redis",
  displayName: "Redis",
  type: "cache",
  description: "In-memory data structure store",
  image: "redis",
  tag: "7-alpine",
  needs: [],
  ports: [{ container: 6379 }],
  requiresProxy: false,
  hasAdminUI: false,
  docsUrl: "https://redis.io/docs/",
  icon: "🔴",
  availableIn: ["cloud-kit"],
};

// =============================================================================
// MEDIA SERVICES
// =============================================================================

export const JELLYFIN: ServiceCatalogEntry = {
  id: "jellyfin",
  displayName: "Jellyfin",
  type: "media",
  description: "Free Software Media System",
  image: "jellyfin/jellyfin",
  tag: "latest",
  needs: ["traefik"],
  ports: [{ container: 8096 }],
  requiresProxy: true,
  hasAdminUI: true,
  docsUrl: "https://jellyfin.org/docs/",
  icon: "📺",
  availableIn: ["basement-kit", "cloud-kit"],
};

export const NEXTCLOUD: ServiceCatalogEntry = {
  id: "nextcloud",
  displayName: "Nextcloud",
  type: "storage",
  description: "Self-hosted productivity platform",
  image: "nextcloud",
  tag: "stable-apache",
  needs: ["traefik", "postgresql"],
  ports: [{ container: 80 }],
  requiresProxy: true,
  hasAdminUI: true,
  docsUrl: "https://docs.nextcloud.com/",
  icon: "☁️",
  availableIn: ["cloud-kit"],
};

// =============================================================================
// SERVICE CATALOG BY STACKKIT
// =============================================================================

export const SERVICE_CATALOG: ServiceCatalogEntry[] = [
  TRAEFIK,
  POCKET_ID,
  VAULTWARDEN,
  IMMICH_SERVER,
  IMMICH_ML,
  IMMICH_POSTGRES,
  IMMICH_REDIS,
  PORTAINER,
  WATCHTOWER,
  HOMEPAGE,
  UPTIME_KUMA,
  DOZZLE,
  POSTGRESQL,
  REDIS,
  JELLYFIN,
  NEXTCLOUD,
];

function normalizeCatalogStackKitRef(kitName: string): string {
  switch (kitName.trim().toLowerCase()) {
    case "":
    case "base-kit":
    case "basement":
    case "basementkit":
      return "basement-kit";
    case "cloud":
    case "cloudkit":
      return "cloud-kit";
    default:
      return kitName;
  }
}

/**
 * Get services available for a specific StackKit
 */
export function getServicesForStackKit(kitName: string): ServiceCatalogEntry[] {
  kitName = normalizeCatalogStackKitRef(kitName);
  return SERVICE_CATALOG.filter((s) => s.availableIn.includes(kitName));
}

/**
 * Get required services for a specific StackKit
 */
export function getRequiredServices(kitName: string): ServiceCatalogEntry[] {
  kitName = normalizeCatalogStackKitRef(kitName);
  return SERVICE_CATALOG.filter(
    (s) => s.availableIn.includes(kitName) && s.required,
  );
}

/**
 * Get recommended services for a specific StackKit
 */
export function getRecommendedServices(kitName: string): ServiceCatalogEntry[] {
  kitName = normalizeCatalogStackKitRef(kitName);
  return SERVICE_CATALOG.filter(
    (s) => s.availableIn.includes(kitName) && s.recommended,
  );
}

/**
 * Get optional services for a specific StackKit
 */
export function getOptionalServices(kitName: string): ServiceCatalogEntry[] {
  return SERVICE_CATALOG.filter(
    (s) => s.availableIn.includes(kitName) && !s.required && !s.recommended,
  );
}

/**
 * Convert catalog entry to ServiceDefinition for spec file
 */
export function toServiceDefinition(
  entry: ServiceCatalogEntry,
  config?: Record<string, unknown>,
): Partial<ServiceDefinition> {
  return {
    name: entry.id,
    type: entry.type,
    needs: entry.needs,
    config,
  };
}
