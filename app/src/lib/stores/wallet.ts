/**
 * Wallet Store - Credential management with server-side filtering
 */
import { writable, derived, get, type Readable } from "svelte/store";
import type { PaginatedState, FilterOptions, ActionResult } from "./types";

export type CredentialType =
  | "password"
  | "api_key"
  | "ssh_key"
  | "oauth_token"
  | "certificate"
  | "other";

export type WalletEntryArea = "tools" | "access" | "recovery";

export type WalletItemClass =
  | "launch"
  | "user_account"
  | "credential"
  | "recovery"
  | "machine_secret_ref"
  | "manual";

export type WalletAccessMode = "open" | "reveal" | "manage";

export type PBWalletItem = {
  id: string;
  collectionId?: string;
  collectionName?: string;
  created?: string;
  updated?: string;
  name: string;
  kind: CredentialType;
  username?: string;
  secret?: string;
  totp?: string;
  notes?: string;
  url?: string;
  stack_id?: string;
  service_id?: string;
  expires_at?: string;
  last_rotated?: string;
  auto_generated?: boolean;
  owner_id?: string;
  item_class?: WalletItemClass;
  source_type?: string;
  source_ref?: string;
  access_mode?: WalletAccessMode;
  revealable?: boolean;
  has_secret?: boolean;
  has_totp?: boolean;
};

export interface WalletFilterOptions extends FilterOptions {
  kind?: CredentialType;
  stackId?: string;
  serviceId?: string;
  expiringSoon?: boolean; // Items expiring in next 30 days
}

interface WalletState extends PaginatedState<PBWalletItem> {
  selectedId: string | null;
}

const DEFAULT_STATE: WalletState = {
  items: [],
  loading: false,
  error: null,
  lastUpdated: null,
  subscribed: false,
  selectedId: null,
  page: 1,
  perPage: 25,
  totalItems: 0,
  totalPages: 0,
  pagination: {
    page: 1,
    perPage: 25,
    totalItems: 0,
    totalPages: 0,
  },
};

function createWalletStore() {
  const state = writable<WalletState>({ ...DEFAULT_STATE });
  let currentFilters: WalletFilterOptions = {};

  // Derived stores
  const items: Readable<PBWalletItem[]> = derived(state, ($s) => $s.items);
  const loading: Readable<boolean> = derived(state, ($s) => $s.loading);
  const error: Readable<string | null> = derived(state, ($s) => $s.error);
  const selectedItem: Readable<PBWalletItem | null> = derived(
    state,
    ($s) => $s.items.find((item) => item.id === $s.selectedId) ?? null,
  );

  function unsupportedWalletStore<T = PBWalletItem[]>(): ActionResult<T> {
    return {
      success: false,
      error:
        "Wallet data must be accessed through backend wallet APIs; this store no longer reads or writes PocketBase wallet records.",
    };
  }

  /**
   * Load wallet items.
   */
  async function load(
    opts: WalletFilterOptions = {},
  ): Promise<ActionResult<PBWalletItem[]>> {
    currentFilters = opts;
    state.update((s) => ({
      ...s,
      loading: false,
      error: null,
      lastUpdated: new Date(),
    }));
    return { success: true, data: get(state).items };
  }

  /**
   * Create a new wallet item
   */
  async function create(
    data: Partial<PBWalletItem>,
  ): Promise<ActionResult<PBWalletItem>> {
    void data;
    const result = unsupportedWalletStore<PBWalletItem>();
    state.update((s) => ({
      ...s,
      loading: false,
      error: result.error ?? null,
    }));
    return result;
  }

  /**
   * Update a wallet item with optimistic update
   */
  async function update(
    id: string,
    data: Partial<PBWalletItem>,
  ): Promise<ActionResult<PBWalletItem>> {
    void id;
    void data;
    const result = unsupportedWalletStore<PBWalletItem>();
    state.update((s) => ({
      ...s,
      loading: false,
      error: result.error ?? null,
    }));
    return result;
  }

  /**
   * Delete a wallet item with optimistic removal
   */
  async function remove(id: string): Promise<ActionResult> {
    void id;
    const result = unsupportedWalletStore<void>();
    state.update((s) => ({
      ...s,
      loading: false,
      error: result.error ?? null,
    }));
    return result;
  }

  /**
   * Rotate a credential's secret
   */
  async function rotateSecret(
    id: string,
    newSecret: string,
  ): Promise<ActionResult<PBWalletItem>> {
    return update(id, {
      secret: newSecret,
      last_rotated: new Date().toISOString(),
    });
  }

  /**
   * Subscribe to real-time updates
   */
  function startSubscription(): () => void {
    if (get(state).subscribed) {
      return stopSubscription;
    }

    state.update((s) => ({ ...s, subscribed: true }));
    void load(currentFilters);
    return stopSubscription;
  }

  /**
   * Unsubscribe from real-time updates
   */
  function stopSubscription(): void {
    state.update((s) => ({ ...s, subscribed: false }));
  }

  /**
   * Set selected item
   */
  function select(id: string | null): void {
    state.update((s) => ({ ...s, selectedId: id }));
  }

  /**
   * Pagination controls
   */
  function setPage(page: number): void {
    state.update((s) => ({
      ...s,
      page,
      pagination: { ...s.pagination, page },
    }));
    load(currentFilters);
  }

  function setPerPage(perPage: number): void {
    state.update((s) => ({
      ...s,
      page: 1,
      perPage,
      pagination: { ...s.pagination, perPage, page: 1 },
    }));
    load(currentFilters);
  }

  /**
   * Utilities
   */
  function clearError(): void {
    state.update((s) => ({ ...s, error: null }));
  }

  function reset(): void {
    stopSubscription();
    state.set({ ...DEFAULT_STATE });
    currentFilters = {};
  }

  return {
    subscribe: state.subscribe,
    items,
    loading,
    error,
    selectedItem,

    load,
    create,
    update,
    remove,
    rotateSecret,

    startSubscription,
    stopSubscription,

    select,
    setPage,
    setPerPage,

    clearError,
    reset,
  };
}

export const walletStore = createWalletStore();
