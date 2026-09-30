/**
 * The owner's dashboard preset. Techstack has no general preferences API, so
 * this follows the navigation preview shape setting (#2377) and stores the
 * choice on the device, keyed by the signed-in owner so two owners sharing a
 * browser keep their own choice.
 */
import { authStore } from "#lib/stores/auth.svelte.js";
import {
  DEFAULT_DASHBOARD_PRESET,
  effectivePreset,
  isDashboardPresetId,
  type DashboardPresetId,
} from "#lib/dashboard/presets.js";

const STORAGE_PREFIX = "techstack-dashboard-preset";

function storageKey(ownerKey: string): string {
  return `${STORAGE_PREFIX}:${ownerKey}`;
}

function readStored(ownerKey: string): DashboardPresetId {
  try {
    const value = localStorage.getItem(storageKey(ownerKey));
    return isDashboardPresetId(value) ? value : DEFAULT_DASHBOARD_PRESET;
  } catch {
    return DEFAULT_DASHBOARD_PRESET;
  }
}

/** The owner the choice belongs to: the Cloud subject, else this instance. */
export function dashboardOwnerKey(): string {
  return authStore.cloudUser?.sub?.trim() || "local";
}

class DashboardPresetStore {
  #stored = $state<Record<string, DashboardPresetId>>({});

  /** The preset to render for the current owner. */
  get current(): DashboardPresetId {
    const ownerKey = dashboardOwnerKey();
    const stored =
      this.#stored[ownerKey] ??
      (typeof localStorage === "undefined"
        ? DEFAULT_DASHBOARD_PRESET
        : readStored(ownerKey));
    return effectivePreset(stored, { ownerKey });
  }

  set(preset: DashboardPresetId) {
    const ownerKey = dashboardOwnerKey();
    try {
      localStorage.setItem(storageKey(ownerKey), preset);
    } catch {
      // Storage can be blocked; the choice still applies for this session.
    }
    this.#stored = { ...this.#stored, [ownerKey]: preset };
  }
}

export const dashboardPreset = new DashboardPresetStore();
