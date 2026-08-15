import { fetchApi } from "./client";
import { parseManagementState, type RegistryManagementState } from "./registry";

export interface InventoryFreshness {
  state: "fresh" | "stale" | "unavailable" | string;
  age_seconds?: number;
  stale_after_seconds: number;
  reason_code?: string;
}

export interface InventoryObservedState {
  state: string;
  reason_code?: string;
  changed_at?: string;
  observed_at?: string;
}

export interface InventoryLifecycleProjection {
  state: string;
  reason_code?: string;
  changed_at?: string;
}

export interface InventoryDesiredProjection {
  state: "running" | "stopped" | "absent" | "unknown";
  reason_code?: string;
  changed_at?: string;
}

export interface InventoryLeaseProjection {
  id?: string;
  state: string;
}

export interface InventoryCleanupProjection {
  state:
    | "not_requested"
    | "delete_accepted"
    | "absence_pending"
    | "absent"
    | "cleanup_required"
    | "failed"
    | "unverified";
  provider_absence_verified: boolean;
  verified_at?: string;
}

export interface InventoryStackKit {
  name?: string;
  version?: string;
  variant?: string;
}

export interface InventoryServer {
  id: string;
  stack_id?: string;
  name: string;
  observed_at?: string;
  freshness: InventoryFreshness;
  inventory_revision: number;
  addresses: {
    public_ips: string[];
    private_ips: string[];
    local_ips: string[];
    domains: string[];
  };
  platform: {
    os?: string;
    os_version?: string;
    arch?: string;
  };
  stackkit: InventoryStackKit;
  provider?: string;
  lifecycle: InventoryLifecycleProjection;
  desired: InventoryDesiredProjection;
  lease: InventoryLeaseProjection;
  cleanup: InventoryCleanupProjection;
  connection: InventoryObservedState;
  health: InventoryObservedState;
}

export interface InventoryServiceLink {
  url: string;
  mode: string;
}

export interface InventoryService {
  id: string;
  stack_id: string;
  server_id: string;
  key: string;
  name: string;
  /**
   * Persisted ownership dimension, REQUIRED by the canonical inventory
   * response (`internal/routes/inventory_api.go` emits it non-omitempty).
   * `desired_state` is only a declared contract while this is `managed`.
   */
  management_state: RegistryManagementState;
  desired_state: string;
  observed_state: string;
  observed_at?: string;
  freshness: InventoryFreshness;
  inventory_revision: number;
  health: InventoryObservedState;
  stackkit: InventoryStackKit;
  links: InventoryServiceLink[];
  allowed_actions: Array<"start" | "stop" | "restart" | "logs" | string>;
  evidence_ref?: string;
  source: string;
}

export interface InventoryServerList {
  observed_at: string;
  freshness: InventoryFreshness;
  inventory_revision: number;
  collection_cursor: string;
  next_cursor?: string;
  page_size: number;
  servers: InventoryServer[];
}

export interface InventoryServiceList {
  observed_at: string;
  freshness: InventoryFreshness;
  inventory_revision: number;
  collection_cursor: string;
  next_cursor?: string;
  page_size: number;
  services: InventoryService[];
}

export interface InventoryPageOptions {
  cursor?: string;
  limit?: number;
}

export interface InventoryServicePageOptions extends InventoryPageOptions {
  serverId?: string;
}

const inventoryClientPageSize = 200;
const maxInventoryClientPages = 100;

function inventoryPagePath(
  path: string,
  options: InventoryServicePageOptions,
): string {
  const query = new URLSearchParams();
  query.set("limit", String(options.limit ?? inventoryClientPageSize));
  if (options.cursor) query.set("cursor", options.cursor);
  if (options.serverId) query.set("server_id", options.serverId);
  return `${path}?${query.toString()}`;
}

export async function listInventoryServerPage(
  options: InventoryPageOptions = {},
): Promise<InventoryServerList> {
  const response = await fetchApi<InventoryServerList>(
    inventoryPagePath("/api/v1/inventory/servers", options),
  );
  return response.data;
}

export async function listInventoryServicePage(
  options: InventoryServicePageOptions = {},
): Promise<InventoryServiceList> {
  const response = await fetchApi<InventoryServiceList>(
    inventoryPagePath("/api/v1/inventory/services", options),
  );
  const page = response.data;
  return {
    ...page,
    // A service whose ownership dimension is missing must not be silently
    // rendered as unmanaged; parseManagementState throws instead.
    services: (page.services ?? []).map((service, index) => ({
      ...service,
      management_state: parseManagementState(
        service.management_state,
        `inventory.services[${index}]`,
      ),
    })),
  };
}

// Existing UI consumers expect the complete collection. Follow the bounded,
// stable cursor traversal here so adding pagination to REST cannot silently
// truncate dashboards that have not introduced visible paging controls yet.
export async function listInventoryServers(): Promise<InventoryServerList> {
  const pages: InventoryServerList[] = [];
  let cursor: string | undefined;
  const seen = new Set<string>();
  for (let pageNumber = 0; pageNumber < maxInventoryClientPages; pageNumber++) {
    const page = await listInventoryServerPage({ cursor });
    pages.push(page);
    if (!page.next_cursor) return mergeServerPages(pages);
    if (seen.has(page.next_cursor)) {
      throw new Error("Inventory server cursor did not advance");
    }
    seen.add(page.next_cursor);
    cursor = page.next_cursor;
  }
  throw new Error("Inventory server traversal exceeded the page limit");
}

export async function listInventoryServices(): Promise<InventoryServiceList> {
  const pages: InventoryServiceList[] = [];
  let cursor: string | undefined;
  const seen = new Set<string>();
  for (let pageNumber = 0; pageNumber < maxInventoryClientPages; pageNumber++) {
    const page = await listInventoryServicePage({ cursor });
    pages.push(page);
    if (!page.next_cursor) return mergeServicePages(pages);
    if (seen.has(page.next_cursor)) {
      throw new Error("Inventory service cursor did not advance");
    }
    seen.add(page.next_cursor);
    cursor = page.next_cursor;
  }
  throw new Error("Inventory service traversal exceeded the page limit");
}

function mergeServerPages(pages: InventoryServerList[]): InventoryServerList {
  const first = pages[0];
  const servers = pages.flatMap((page) => page.servers);
  return {
    observed_at: first.observed_at,
    freshness: aggregateFreshness(servers),
    inventory_revision: Math.max(
      0,
      ...servers.map((item) => item.inventory_revision),
    ),
    collection_cursor: first.collection_cursor,
    page_size: servers.length,
    servers,
  };
}

function mergeServicePages(
  pages: InventoryServiceList[],
): InventoryServiceList {
  const first = pages[0];
  const services = pages.flatMap((page) => page.services);
  return {
    observed_at: first.observed_at,
    freshness: aggregateFreshness(services),
    inventory_revision: Math.max(
      0,
      ...services.map((item) => item.inventory_revision),
    ),
    collection_cursor: first.collection_cursor,
    page_size: services.length,
    services,
  };
}

function aggregateFreshness(
  items: Array<{ freshness: InventoryFreshness }>,
): InventoryFreshness {
  const staleAfter = items[0]?.freshness.stale_after_seconds ?? 0;
  if (items.length === 0) {
    return {
      state: "unavailable",
      stale_after_seconds: staleAfter,
      reason_code: "no_inventory",
    };
  }
  if (items.some((item) => item.freshness.state === "unavailable")) {
    return {
      state: "unavailable",
      stale_after_seconds: staleAfter,
      reason_code: "inventory_observation_unavailable",
    };
  }
  if (items.some((item) => item.freshness.state === "stale")) {
    return {
      state: "stale",
      stale_after_seconds: staleAfter,
      reason_code: "inventory_observation_stale",
    };
  }
  return { state: "fresh", stale_after_seconds: staleAfter };
}
