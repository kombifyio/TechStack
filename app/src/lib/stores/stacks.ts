/**
 * Stacks Store - Stack management with aggregated node/service counts
 */
import { writable, derived, get, type Readable } from "svelte/store";
import { createStack, listStacks, type Stack } from "$lib/api/stacks";
import type { CollectionState, FilterOptions, ActionResult } from "./types";

export type StackStatus =
  | "draft"
  | "pending"
  | "provisioning"
  | "running"
  | "stopping"
  | "stopped"
  | "error";

export type StackMode = "easy" | "techie";

export type PBStack = {
  id: string;
  collectionId: string;
  collectionName: "stacks";
  created: string;
  updated: string;
  name: string;
  description?: string;
  mode: StackMode;
  status: StackStatus;
  config?: Record<string, unknown>;
  services?: string[];
  owner_id: string;
  runtime_phase?: string;
  server_mode?: string;
  runtime_lane?: string;
  runtime_offering_id?: string;
  provider_id?: string;
  /** Historical non-authoritative provider label. */
  lease_provider?: string;
  ionos_datacenter?: string;
  provider_region?: string;
  simulate_provider_id?: string;
  simulate_node_lifecycle?: string;
  lease_id?: string;
  desired_state?: string;
  billing_mode?: string;
  billing_cadence?: string;
  catalog_ref?: string;
  stackkit_catalog_ref?: string;
  verification_status?: string;
  server_provisioning_mode?: string;
  server_connection_mode?: string;
  server_remote_host_present?: boolean;
  server_remote_user_present?: boolean;
  server_remote_auth_method?: string;
  server_remote_credential_ref?: string;
  server_remote_use_sudo?: boolean;
  server_install_command_required?: boolean;
};

export interface StackWithCounts extends PBStack {
  nodeCount: number;
  serviceCount: number;
}

export interface StackFilterOptions extends FilterOptions {
  status?: StackStatus;
  mode?: "easy" | "techie";
  ownerId?: string;
}

interface StacksState extends CollectionState<StackWithCounts> {
  selectedId: string | null;
}

const DEFAULT_STATE: StacksState = {
  items: [],
  loading: false,
  error: null,
  lastUpdated: null,
  subscribed: false,
  selectedId: null,
};

function createStacksStore() {
  const state = writable<StacksState>({ ...DEFAULT_STATE });

  // Derived stores
  const items: Readable<StackWithCounts[]> = derived(state, ($s) => $s.items);
  const loading: Readable<boolean> = derived(state, ($s) => $s.loading);
  const error: Readable<string | null> = derived(state, ($s) => $s.error);
  const selectedStack: Readable<StackWithCounts | null> = derived(
    state,
    ($s) => $s.items.find((item) => item.id === $s.selectedId) ?? null,
  );

  // Grouped by status
  const byStatus: Readable<Record<StackStatus, StackWithCounts[]>> = derived(
    state,
    ($s) => {
      const grouped: Record<StackStatus, StackWithCounts[]> = {
        draft: [],
        pending: [],
        provisioning: [],
        running: [],
        stopping: [],
        stopped: [],
        error: [],
      };

      $s.items.forEach((stack) => {
        if (grouped[stack.status]) {
          grouped[stack.status].push(stack);
        }
      });

      return grouped;
    },
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

  function unsupportedStackMutation<T = PBStack>(): ActionResult<T> {
    return {
      success: false,
      error:
        "Stack mutations are owned by the backend stack APIs; this store no longer writes PocketBase stack records.",
    };
  }

  function stackStatusFromApi(stack: Stack): StackStatus {
    const candidate = stack.state || "draft";
    if (
      candidate === "draft" ||
      candidate === "pending" ||
      candidate === "provisioning" ||
      candidate === "running" ||
      candidate === "stopping" ||
      candidate === "stopped" ||
      candidate === "error"
    ) {
      return candidate;
    }
    return "draft";
  }

  function apiStackToPBStack(stack: Stack): StackWithCounts {
    return {
      id: stack.id,
      collectionId: "",
      collectionName: "stacks",
      created: stack.created_at,
      updated: stack.updated_at,
      name: stack.name,
      mode: "easy",
      status: stackStatusFromApi(stack),
      services: stack.services ?? [],
      owner_id: "",
      runtime_phase: stack.runtime_phase,
      server_mode: stack.server_mode,
      runtime_lane: stack.runtime_lane,
      runtime_offering_id: stack.runtime_offering_id,
      provider_id: stack.provider_id,
      lease_provider: stack.lease_provider,
      ionos_datacenter: stack.ionos_datacenter,
      provider_region: stack.provider_region,
      simulate_provider_id: stack.simulate_provider_id,
      simulate_node_lifecycle: stack.simulate_node_lifecycle,
      lease_id: stack.lease_id,
      desired_state: stack.desired_state,
      billing_mode: stack.billing_mode,
      billing_cadence: stack.billing_cadence,
      catalog_ref: stack.catalog_ref,
      stackkit_catalog_ref: stack.stackkit_catalog_ref,
      verification_status: stack.verification_status,
      server_provisioning_mode: stack.server_provisioning_mode,
      server_connection_mode: stack.server_connection_mode,
      server_remote_host_present: stack.server_remote_host_present,
      server_remote_user_present: stack.server_remote_user_present,
      server_remote_auth_method: stack.server_remote_auth_method,
      server_remote_credential_ref: stack.server_remote_credential_ref,
      server_remote_use_sudo: stack.server_remote_use_sudo,
      server_install_command_required: stack.server_install_command_required,
      nodeCount: 0,
      serviceCount: 0,
    };
  }

  function filterStacks(
    stacks: StackWithCounts[],
    opts: StackFilterOptions,
  ): StackWithCounts[] {
    const search = opts.search?.trim().toLowerCase();
    return stacks.filter((stack) => {
      if (opts.status && stack.status !== opts.status) return false;
      if (opts.mode && stack.mode !== opts.mode) return false;
      if (opts.ownerId && stack.owner_id !== opts.ownerId) return false;
      if (search) {
        const haystack =
          `${stack.name} ${stack.description ?? ""}`.toLowerCase();
        if (!haystack.includes(search)) return false;
      }
      return true;
    });
  }

  /**
   * Load stacks through the Go BFF.
   */
  async function load(
    opts: StackFilterOptions = {},
  ): Promise<ActionResult<StackWithCounts[]>> {
    state.update((s) => ({ ...s, loading: true, error: null }));

    try {
      let stacksWithCounts = filterStacks(
        (await listStacks()).map(apiStackToPBStack),
        opts,
      );

      if (opts.sortField) {
        const field = opts.sortField as keyof StackWithCounts;
        const direction = opts.sortOrder === "desc" ? -1 : 1;
        stacksWithCounts = [...stacksWithCounts].sort(
          (a, b) =>
            String(a[field] ?? "").localeCompare(String(b[field] ?? "")) *
            direction,
        );
      }

      state.update((s) => ({
        ...s,
        items: stacksWithCounts,
        loading: false,
        lastUpdated: new Date(),
      }));

      return { success: true, data: stacksWithCounts };
    } catch (err) {
      if (isCancelledRequestError(err)) {
        return { success: true, data: get(state).items };
      }

      const message = apiErrorMessage(err);
      state.update((s) => ({ ...s, loading: false, error: message }));
      return { success: false, error: message };
    }
  }

  /**
   * Create a new stack
   */
  async function create(
    data: Partial<PBStack>,
  ): Promise<ActionResult<PBStack>> {
    state.update((s) => ({ ...s, loading: true, error: null }));

    try {
      const response = await createStack({
        name: data.name ?? "Untitled Stack",
        mode: data.mode,
        services: data.services ?? [],
        user_config: data.config,
      });
      const now = new Date().toISOString();
      const stackWithCounts: StackWithCounts = {
        id: response.stack_id,
        collectionId: "",
        collectionName: "stacks",
        created: now,
        updated: now,
        name: response.name,
        mode: data.mode ?? "easy",
        status: stackStatusFromApi({
          id: response.stack_id,
          name: response.name,
          provider: "",
          state: response.state,
          services: data.services ?? [],
          created_at: now,
          updated_at: now,
        }),
        config: data.config,
        services: data.services ?? [],
        owner_id: data.owner_id ?? "",
        nodeCount: 0,
        serviceCount: 0,
      };

      state.update((s) => ({
        ...s,
        items: [stackWithCounts, ...s.items],
        loading: false,
        lastUpdated: new Date(),
      }));

      return { success: true, data: stackWithCounts };
    } catch (err) {
      const message = apiErrorMessage(err);
      state.update((s) => ({ ...s, loading: false, error: message }));
      return { success: false, error: message };
    }
  }

  /**
   * Update a stack with optimistic update
   */
  async function update(
    id: string,
    data: Partial<PBStack>,
  ): Promise<ActionResult<PBStack>> {
    void id;
    void data;
    const result = unsupportedStackMutation();
    state.update((s) => ({
      ...s,
      loading: false,
      error: result.error ?? null,
    }));
    return result;
  }

  /**
   * Delete a stack
   */
  async function remove(id: string): Promise<ActionResult> {
    void id;
    const result = unsupportedStackMutation<void>();
    state.update((s) => ({
      ...s,
      loading: false,
      error: result.error ?? null,
    }));
    return result;
  }

  /**
   * Get a single stack by ID
   */
  async function getOne(id: string): Promise<ActionResult<PBStack>> {
    try {
      const cached = get(state).items.find((item) => item.id === id);
      const record =
        cached ??
        (await listStacks()).map(apiStackToPBStack).find((s) => s.id === id);
      if (!record) {
        return { success: false, error: "Stack not found" };
      }
      return { success: true, data: record };
    } catch (err) {
      return { success: false, error: apiErrorMessage(err) };
    }
  }

  /**
   * Subscribe to real-time updates
   */
  function startSubscription(): () => void {
    if (get(state).subscribed) {
      return stopSubscription;
    }

    state.update((s) => ({ ...s, subscribed: true }));
    void load();
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
    selectedStack,
    byStatus,

    load,
    create,
    update,
    remove,
    getOne,

    startSubscription,
    stopSubscription,

    select,
    clearError,
    reset,
  };
}

export const stacksStore = createStacksStore();
