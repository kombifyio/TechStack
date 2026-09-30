import { browser } from "$app/env";
import {
  getStackIdentitySettings,
  syncStackIdentityStep,
  updateStackIdentitySettings,
  type StackIdentitySettingsResponse,
} from "#lib/api/auth.js";
import {
  readCloudStackIdentity,
  writeCloudStackIdentity,
} from "#lib/api/cloudStackIdentity.js";
import { writable } from "svelte/store";
import {
  normalizeStackIdentity,
  type StackIdentity,
} from "#lib/components/open-core/index.js";
export type { StackIdentity };

const STORAGE_KEY = "techstack.stack-identity";

const { subscribe, set } = writable<StackIdentity | null>(null);

// Only the name is required. The character and animation are presentation
// defaults, so an identity kombify Cloud sends without them still names the
// homelab.
function normalizeIdentity(
  identity: Partial<StackIdentity> | null | undefined,
): StackIdentity | null {
  const name = identity?.name?.trim();
  if (!name) {
    return null;
  }

  const normalized = normalizeStackIdentity(identity);
  return {
    name,
    characterId: normalized.characterId,
    animationStyle: normalized.animationStyle,
    savedAt: normalized.savedAt,
    animationEnabled: normalized.animationEnabled,
    iconStyle: normalized.iconStyle,
    glowColorOverride: normalized.glowColorOverride,
  };
}

export function initStackIdentity(): void {
  if (!browser) return;

  try {
    const raw = window.sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return;

    const parsed = JSON.parse(raw) as StackIdentity;
    set(normalizeIdentity(parsed));
  } catch {
    window.sessionStorage.removeItem(STORAGE_KEY);
    set(null);
  }
}

export function setStackIdentity(
  identity: StackIdentity | null | undefined,
): void {
  const normalized = normalizeIdentity(identity);
  set(normalized);

  if (!browser) return;

  if (!normalized) {
    window.sessionStorage.removeItem(STORAGE_KEY);
    return;
  }

  window.sessionStorage.setItem(STORAGE_KEY, JSON.stringify(normalized));
}

export function clearStackIdentity(): void {
  set(null);
  if (browser) {
    window.sessionStorage.removeItem(STORAGE_KEY);
  }
}

/**
 * Load the homelab's Stack Identity (the one local copy, also the homelab
 * name), then sync it with kombify Cloud when Cloud is reachable. A local
 * answer without an identity does not clear one the Cloud host already
 * handed over (launch token or embed message) before the sync ran.
 */
export async function hydrateStackIdentityFromBackend(): Promise<StackIdentitySettingsResponse | null> {
  let response: StackIdentitySettingsResponse | null = null;
  try {
    response = await getStackIdentitySettings();
    if (response.stack_identity) setStackIdentity(response.stack_identity);
  } catch {
    response = null;
  }
  const synced = await syncStackIdentityWithCloud();
  return synced ?? response;
}

export async function saveStackIdentityToBackend(
  identity: StackIdentity,
): Promise<StackIdentitySettingsResponse> {
  const response = await updateStackIdentitySettings(identity);
  setStackIdentity(response.stack_identity ?? identity);
  void syncStackIdentityWithCloud();
  return response;
}

let syncInFlight: Promise<StackIdentitySettingsResponse | null> | null = null;

/**
 * One kombify Cloud sync (STACK-IDENTITY-CUSTOMIZATION-STANDARD §8): read
 * Cloud's record through the Gateway with the user's token, let the backend
 * apply the rules to the homelab row, and push the local copy when it says
 * so. A stale If-Match (both sides changed meanwhile) re-reads once. Without
 * a Gateway token Techstack is the only authority and nothing happens.
 */
export function syncStackIdentityWithCloud(): Promise<StackIdentitySettingsResponse | null> {
  if (!browser) return Promise.resolve(null);
  syncInFlight ??= runCloudSync()
    .catch(() => null)
    .finally(() => {
      syncInFlight = null;
    });
  return syncInFlight;
}

async function runCloudSync(): Promise<StackIdentitySettingsResponse | null> {
  for (let attempt = 0; attempt < 2; attempt += 1) {
    const read = await readCloudStackIdentity();
    if (read.state === "unavailable") return null;
    const step = await syncStackIdentityStep(
      read.state === "found" ? read.identity : null,
    );
    if (step.stack_identity) setStackIdentity(step.stack_identity);
    if (step.action !== "push_local" || !step.push) return step;

    const written = await writeCloudStackIdentity(
      step.push.body,
      step.push.if_match,
    );
    if (written.state === "conflict") continue;
    if (written.state !== "saved") return step;
    const confirmed = await syncStackIdentityStep(written.identity);
    if (confirmed.stack_identity) setStackIdentity(confirmed.stack_identity);
    return confirmed;
  }
  return null;
}

export const stackIdentity = {
  subscribe,
};
