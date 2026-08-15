import { browser } from "$app/environment";
import {
  getStackIdentitySettings,
  updateStackIdentitySettings,
  type StackIdentitySettingsResponse,
} from "$lib/api/auth";
import { writable } from "svelte/store";
import {
  normalizeStackIdentity,
  type StackIdentity,
} from "$lib/components/open-core";
export type { StackIdentity };

const STORAGE_KEY = "techstack.stack-identity";

const { subscribe, set } = writable<StackIdentity | null>(null);

function normalizeIdentity(
  identity: StackIdentity | null | undefined,
): StackIdentity | null {
  if (!identity?.name || !identity?.characterId) {
    return null;
  }

  const normalized = normalizeStackIdentity(identity);
  return {
    name: identity.name,
    characterId: normalized.characterId,
    animationStyle: normalized.animationStyle,
    savedAt: identity.savedAt ?? null,
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

export async function hydrateStackIdentityFromBackend(): Promise<StackIdentitySettingsResponse | null> {
  try {
    const response = await getStackIdentitySettings();
    setStackIdentity(response.stack_identity ?? null);
    return response;
  } catch {
    return null;
  }
}

export async function saveStackIdentityToBackend(
  identity: StackIdentity,
): Promise<StackIdentitySettingsResponse> {
  const response = await updateStackIdentitySettings(identity);
  setStackIdentity(response.stack_identity ?? identity);
  return response;
}

export const stackIdentity = {
  subscribe,
};
