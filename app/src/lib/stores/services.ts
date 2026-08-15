/**
 * Services Store - Service management with type/status filtering
 */
import { writable, derived, get, type Readable } from "svelte/store";
import { listServiceRegistry, type RegistryService } from "$lib/api/registry";
import type { CollectionState, FilterOptions, ActionResult } from "./types";

export type ServiceType =
  | "pocketbase"
  | "headscale"
  | "monitoring"
  | "traefik"
  | "auth"
  | "identity"
  | "ingress"
  | "reverse-proxy"
  | "media"
  | "database"
  | "cache"
  | "storage"
  | "secrets"
  | "custom";

export type ServiceStatus =
  | "pending"
  | "running"
  | "stopped"
  | "error"
  | "observed"
  | "adopting";

export type PBService = {
  id: string;
  collectionId: string;
  collectionName: "services";
  created: string;
  updated: string;
  name: string;
  display_name?: string;
  type: ServiceType;
  port?: number;
  url?: string;
  status: ServiceStatus;
  node_id: string;
  stack_id?: string;
  stack_name?: string;
  node_name?: string;
};

export interface ServiceFilterOptions extends FilterOptions {
  nodeId?: string;
  stackId?: string;
  status?: ServiceStatus;
  serviceType?: string;
}

interface ServicesState extends CollectionState<PBService> {
  selectedId: string | null;
}

const DEFAULT_STATE: ServicesState = {
  items: [],
  loading: false,
  error: null,
  lastUpdated: null,
  subscribed: false,
  selectedId: null,
};

function createServicesStore() {
  const state = writable<ServicesState>({ ...DEFAULT_STATE });

  // Derived stores
  const items: Readable<PBService[]> = derived(state, ($s) => $s.items);
  const loading: Readable<boolean> = derived(state, ($s) => $s.loading);
  const error: Readable<string | null> = derived(state, ($s) => $s.error);
  const selectedService: Readable<PBService | null> = derived(
    state,
    ($s) => $s.items.find((item) => item.id === $s.selectedId) ?? null,
  );

  // Grouped by status
  const byStatus: Readable<Record<ServiceStatus, PBService[]>> = derived(
    state,
    ($s) => {
      const grouped: Record<ServiceStatus, PBService[]> = {
        pending: [],
        running: [],
        stopped: [],
        error: [],
        observed: [],
        adopting: [],
      };

      $s.items.forEach((service) => {
        if (grouped[service.status]) {
          grouped[service.status].push(service);
        }
      });

      return grouped;
    },
  );

  // Grouped by type
  const byType: Readable<Map<string, PBService[]>> = derived(state, ($s) => {
    const grouped = new Map<string, PBService[]>();

    $s.items.forEach((service) => {
      const serviceType =
        (service as PBService & { service_type?: string }).service_type ||
        service.type;
      const list = grouped.get(serviceType) || [];
      list.push(service);
      grouped.set(serviceType, list);
    });

    return grouped;
  });

  // Services for a specific node
  const forNode = (nodeId: string): Readable<PBService[]> =>
    derived(state, ($s) => $s.items.filter((item) => item.node_id === nodeId));

  // Services for a specific stack (via nodes - requires expand)
  const forStack = (stackId: string): Readable<PBService[]> =>
    derived(state, ($s) =>
      $s.items.filter((item) => item.stack_id === stackId),
    );

  function isCancelledRequestError(err: unknown): boolean {
    const maybeAny = err as { name?: unknown; message?: unknown } | null;
    const name = typeof maybeAny?.name === "string" ? maybeAny.name : "";
    const message =
      err instanceof Error
        ? err.message
        : typeof maybeAny?.message === "string"
          ? maybeAny.message
          : "";

    return (
      name === "AbortError" ||
      message.toLowerCase().includes("autocancel") ||
      message.toLowerCase().includes("aborted")
    );
  }

  function apiErrorMessage(err: unknown): string {
    return err instanceof Error && err.message ? err.message : "Request failed";
  }

  function unsupportedServiceMutation<T = PBService>(): ActionResult<T> {
    return {
      success: false,
      error:
        "Service mutations are owned by the backend registry APIs; this store no longer writes PocketBase service records.",
    };
  }

  /**
   * Load services with optional filtering
   */
  async function load(
    opts: ServiceFilterOptions = {},
  ): Promise<ActionResult<PBService[]>> {
    state.update((s) => ({ ...s, loading: true, error: null }));

    try {
      const registry = await listServiceRegistry();
      const result = filterRegistryServices(
        registry.services.map(registryServiceToPBService),
        opts,
      );

      state.update((s) => ({
        ...s,
        items: result,
        loading: false,
        lastUpdated: new Date(),
      }));

      return { success: true, data: result };
    } catch (err) {
      if (isCancelledRequestError(err)) {
        return { success: true, data: get(state).items };
      }

      const message = apiErrorMessage(err);
      state.update((s) => ({ ...s, loading: false, error: message }));
      return { success: false, error: message };
    }
  }

  function registryServiceToPBService(service: RegistryService): PBService {
    return {
      id:
        service.id ||
        `${service.stack_id}-${service.server_id}-${service.name}`,
      collectionId: "",
      collectionName: "services",
      created: "",
      updated: "",
      name: service.name,
      display_name: service.display_name,
      type: service.type as PBService["type"],
      port: service.port,
      url: service.url,
      status: service.status as PBService["status"],
      node_id: service.server_id,
      stack_id: service.stack_id,
      stack_name: service.stack_name,
      node_name: service.server_name,
    };
  }

  function filterRegistryServices(
    items: PBService[],
    opts: ServiceFilterOptions,
  ): PBService[] {
    const search = opts.search?.trim().toLowerCase();
    const filtered = items.filter((item) => {
      if (opts.nodeId && item.node_id !== opts.nodeId) return false;
      if (
        opts.stackId &&
        (item as PBService & { stack_id?: string }).stack_id !== opts.stackId
      ) {
        return false;
      }
      if (opts.status && item.status !== opts.status) return false;
      if (opts.serviceType && item.type !== opts.serviceType) return false;
      if (search) {
        const haystack =
          `${item.name} ${item.display_name || ""} ${item.type}`.toLowerCase();
        if (!haystack.includes(search)) return false;
      }
      return true;
    });

    const sortField = opts.sortField || "name";
    const direction = opts.sortOrder === "desc" ? -1 : 1;
    return filtered.sort((a, b) => {
      const aValue = String(
        (a as unknown as Record<string, unknown>)[sortField] ?? "",
      );
      const bValue = String(
        (b as unknown as Record<string, unknown>)[sortField] ?? "",
      );
      return aValue.localeCompare(bValue) * direction;
    });
  }

  /**
   * Load services for a specific node
   */
  async function loadByNode(
    nodeId: string,
  ): Promise<ActionResult<PBService[]>> {
    return load({ nodeId });
  }

  /**
   * Load services for a specific stack
   */
  async function loadByStack(
    stackId: string,
  ): Promise<ActionResult<PBService[]>> {
    return load({ stackId });
  }

  /**
   * Create a new service
   */
  async function create(
    data: Partial<PBService>,
  ): Promise<ActionResult<PBService>> {
    void data;
    const result = unsupportedServiceMutation();
    state.update((s) => ({
      ...s,
      loading: false,
      error: result.error ?? null,
    }));
    return result;
  }

  /**
   * Update a service with optimistic update
   */
  async function update(
    id: string,
    data: Partial<PBService>,
  ): Promise<ActionResult<PBService>> {
    void id;
    void data;
    const result = unsupportedServiceMutation();
    state.update((s) => ({
      ...s,
      loading: false,
      error: result.error ?? null,
    }));
    return result;
  }

  /**
   * Delete a service
   */
  async function remove(id: string): Promise<ActionResult> {
    void id;
    const result = unsupportedServiceMutation<void>();
    state.update((s) => ({
      ...s,
      loading: false,
      error: result.error ?? null,
    }));
    return result;
  }

  /**
   * Get a single service by ID
   */
  async function getOne(id: string): Promise<ActionResult<PBService>> {
    try {
      const cached = get(state).items.find((item) => item.id === id);
      const record =
        cached ??
        (await listServiceRegistry()).services
          .map(registryServiceToPBService)
          .find((service) => service.id === id);
      if (!record) {
        return { success: false, error: "Service not found" };
      }
      return { success: true, data: record };
    } catch (err) {
      return { success: false, error: apiErrorMessage(err) };
    }
  }

  /**
   * Start a service
   */
  async function start(id: string): Promise<ActionResult<PBService>> {
    return update(id, { status: "pending" }); // Status transition handled by backend
  }

  /**
   * Stop a service
   */
  async function stop(id: string): Promise<ActionResult<PBService>> {
    return update(id, { status: "stopped" });
  }

  /**
   * Restart a service
   */
  async function restart(id: string): Promise<ActionResult<PBService>> {
    const result = await stop(id);
    if (!result.success) return result;
    return start(id);
  }

  /**
   * Subscribe to real-time updates
   */
  function startSubscription(nodeId?: string): () => void {
    if (get(state).subscribed) {
      return stopSubscription;
    }

    state.update((s) => ({ ...s, subscribed: true }));
    void load(nodeId ? { nodeId } : {});
    return stopSubscription;
  }

  function stopSubscription(): void {
    state.update((s) => ({ ...s, subscribed: false }));
  }

  function select(id: string | null): void {
    state.update((s) => ({ ...s, selectedId: id }));
  }

  function clearError(): void {
    state.update((s) => ({ ...s, error: null }));
  }

  function reset(): void {
    stopSubscription();
    state.set({ ...DEFAULT_STATE });
  }

  return {
    subscribe: state.subscribe,
    items,
    loading,
    error,
    selectedService,
    byStatus,
    byType,
    forNode,
    forStack,

    load,
    loadByNode,
    loadByStack,
    create,
    update,
    remove,
    getOne,

    start,
    stop,
    restart,

    startSubscription,
    stopSubscription,

    select,
    clearError,
    reset,
  };
}

export const servicesStore = createServicesStore();
