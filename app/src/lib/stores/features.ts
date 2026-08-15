/**
 * Feature Flags Store
 *
 * Read-only reactive access to feature flag state. Flags are managed
 * centrally (Cloudflare Edge/OpenFeature entitlements); the app only loads and evaluates them.
 */
import { writable, derived, get, type Readable } from "svelte/store";
import {
  listFeatures,
  type FeatureFlag,
  type FeaturesResponse,
  type FeatureCategory,
  type RiskLevel,
} from "$lib/api/features";
import { ApiRequestError } from "$lib/api/client";

// Distinguishes a transient/auth verification failure (retryable, self-heals)
// from a genuine not-entitled state. auth=401/403, network=no response, server=5xx.
export type FeaturesErrorKind = "auth" | "network" | "server" | null;

function classifyFeaturesError(err: unknown): FeaturesErrorKind {
  if (!(err instanceof ApiRequestError) || err.status === undefined)
    return "network";
  if (err.status === 401 || err.status === 403) return "auth";
  return "server";
}

// Cache duration in milliseconds (5 seconds for quick interactions, longer for background)
const CACHE_DURATION_MS = 5000;
const BACKGROUND_CACHE_DURATION_MS = 30000;

// State interface
interface FeaturesState {
  security: FeatureFlag[];
  beta: FeatureFlag[];
  ux: FeatureFlag[];
  loading: boolean;
  error: string | null;
  /** Classification of `error` so the UI can offer retry vs not-entitled. */
  errorKind: FeaturesErrorKind;
  lastUpdated: Date | null;
  /** True once features have been loaded at least once (prevents false-disabled flicker) */
  ready: boolean;
}

// Initial state
const DEFAULT_STATE: FeaturesState = {
  security: [],
  beta: [],
  ux: [],
  loading: false,
  error: null,
  errorKind: null,
  lastUpdated: null,
  ready: false,
};

/**
 * Default values for features when not yet loaded.
 * These MUST match the backend Go definitions in pkg/features/flags.go.
 * Security/Beta features default to OFF (security-by-default).
 * UX features default to ON (usability-by-default).
 */
const FEATURE_DEFAULTS: Record<string, boolean> = {
  // Security features - OFF by default (require explicit opt-in)
  network_discovery: false,
  raw_commands: false,
  ssh_tunnel: false,
  cloud_backup: false,
  // The Alpha desktop's native StackKits creation path is the supported local path.
  native_v2_wizard: true,
  cloudflare_tunnel: false,
  self_healing: false,
  ha_stackkit: false,
  monthly_runtime: false,
  monthly_runtime_cloudkit: false,
  monthly_runtime_centron: false,
  monthly_runtime_ionos: false,
  // UX features - ON by default
  onboarding_wizard: true,
  keyboard_shortcuts: true,
  dark_mode: true,
  use_case_photos: true,
  use_case_media: true,
  use_case_vault: true,
  use_case_files: true,
  use_case_smart_home: true,
  use_case_ai: true,
  use_case_dev: true,
  use_case_mail: true,
  use_case_game: true,
};

/**
 * Get the default value for a feature when not yet loaded from backend.
 * Returns the security-conscious default from FEATURE_DEFAULTS.
 */
export function getDefaultEnabled(featureKey: string): boolean {
  return FEATURE_DEFAULTS[featureKey] ?? false;
}

/**
 * Create the features store
 */
function createFeaturesStore() {
  const state = writable<FeaturesState>({ ...DEFAULT_STATE });
  let inFlightLoad: Promise<void> | null = null;

  // Derived stores for convenience
  const security: Readable<FeatureFlag[]> = derived(state, ($s) => $s.security);
  const beta: Readable<FeatureFlag[]> = derived(state, ($s) => $s.beta);
  const ux: Readable<FeatureFlag[]> = derived(state, ($s) => $s.ux);
  const loading: Readable<boolean> = derived(state, ($s) => $s.loading);
  const error: Readable<string | null> = derived(state, ($s) => $s.error);
  const errorKind: Readable<FeaturesErrorKind> = derived(
    state,
    ($s) => $s.errorKind,
  );
  /** True once features have been successfully loaded at least once */
  const ready: Readable<boolean> = derived(state, ($s) => $s.ready);

  // Get all features as a flat map using the definition key (short form)
  const allFeatures: Readable<Map<string, FeatureFlag>> = derived(
    state,
    ($s) => {
      const map = new Map<string, FeatureFlag>();
      [...$s.security, ...$s.beta, ...$s.ux].forEach((f) => {
        // Use the definition key from the feature map (e.g., "network_discovery")
        // The backend maps these to the full keys internally
        // We expose both the short key and full key for compatibility
        map.set(f.key, f);
      });
      return map;
    },
  );

  /**
   * Load features from the API
   * @param forceRefresh - Always fetch from server
   * @param isBackground - Use longer cache duration for background refreshes
   */
  async function load(
    forceRefresh = false,
    isBackground = false,
  ): Promise<void> {
    // De-dupe concurrent loads (layout + page mounts can trigger multiple times).
    // This prevents backend rate limiting (429) and avoids stuck `loading` from
    // overlapping requests.
    if (inFlightLoad) return inFlightLoad;

    const current = get(state);
    const cacheDuration = isBackground
      ? BACKGROUND_CACHE_DURATION_MS
      : CACHE_DURATION_MS;

    // Skip if recently loaded and not forcing refresh
    if (
      !forceRefresh &&
      current.lastUpdated &&
      Date.now() - current.lastUpdated.getTime() < cacheDuration
    ) {
      return;
    }

    state.update((s) => ({
      ...s,
      loading: true,
      error: null,
      errorKind: null,
    }));

    inFlightLoad = (async () => {
      try {
        const data = await listFeatures();
        // error/errorKind were already cleared at load start.
        state.update((s) => ({
          ...s,
          security: data.security || [],
          beta: data.beta || [],
          ux: data.ux || [],
          loading: false,
          lastUpdated: new Date(),
          ready: true,
        }));
      } catch (err) {
        const message =
          err instanceof Error ? err.message : "Failed to load features";
        state.update((s) => ({
          ...s,
          loading: false,
          error: message,
          errorKind: classifyFeaturesError(err),
        }));
        console.error("Failed to load features:", err);
      }
    })().finally(() => {
      inFlightLoad = null;
    });

    return inFlightLoad;
  }

  /**
   * Check if a specific feature is enabled.
   */
  function isEnabled(featureKey: string): boolean {
    const current = get(state);
    const feature = get(allFeatures).get(featureKey);

    if (!current.ready) {
      return getDefaultEnabled(featureKey);
    }

    return feature?.enabled ?? getDefaultEnabled(featureKey);
  }

  /**
   * Get a specific feature by key
   */
  function getFeature(featureKey: string): FeatureFlag | undefined {
    return get(allFeatures).get(featureKey);
  }

  /**
   * Reset store to initial state
   */
  function reset(): void {
    state.set({ ...DEFAULT_STATE });
  }

  return {
    subscribe: state.subscribe,
    security,
    beta,
    ux,
    loading,
    error,
    errorKind,
    ready,
    allFeatures,
    load,
    isEnabled,
    getFeature,
    reset,
  };
}

// Export singleton store
export const features = createFeaturesStore();

// Convenience function for loading features
// (kept backward-compatible while allowing callers to force refresh)
export const loadFeatures = (forceRefresh = false, isBackground = false) =>
  features.load(forceRefresh, isBackground);

// Derived store for quick access to the network-discovery feature.
// Uses FEATURE_DEFAULTS for the fallback to ensure consistent behavior before load.
export const isNetworkDiscoveryEnabled = derived(
  features.allFeatures,
  ($features) =>
    $features.get("network_discovery")?.enabled ??
    getDefaultEnabled("network_discovery"),
);

// Derived store for the native v2 wizard beta flag (wizard-runs facade).
export const isNativeV2WizardEnabled = derived(
  features.allFeatures,
  ($features) =>
    $features.get("native_v2_wizard")?.enabled ??
    getDefaultEnabled("native_v2_wizard"),
);

// Helper type exports
export type { FeatureFlag, FeaturesResponse, FeatureCategory, RiskLevel };
