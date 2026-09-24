import { browser } from "$app/env";
import { clearAutoReloginMarker } from "#lib/auth/session-recovery.js";
import { clearStackIdentity } from "#lib/stores/stackIdentity.js";
import { clearSectionCache } from "#lib/data/sectionCache.js";

const CREATING_SESSION_KEYS = new Set([
  "creatingOperation",
  "creatingJobId",
  "creatingStackId",
  "creatingStackName",
  "creatingStackConfig",
]);

export function clearTechstackSecuritySessionState(): void {
  clearStackIdentity();
  clearCreatingSessionState();
  clearAutoReloginMarker();
  // Cached page sections hold tenant inventory, and sessionStorage survives a
  // sign-out in the same tab. Signing in as somebody else must not paint the
  // previous account's servers, however briefly.
  clearSectionCache();
}

export function clearCreatingSessionState(): void {
  if (!browser) return;
  const storage = window.sessionStorage;
  const keysToRemove: string[] = [];

  for (let index = 0; index < storage.length; index += 1) {
    const key = storage.key(index);
    if (!key) continue;
    if (CREATING_SESSION_KEYS.has(key) || key.startsWith("creating:")) {
      keysToRemove.push(key);
    }
  }

  for (const key of keysToRemove) {
    storage.removeItem(key);
  }
}

export function clearCreatingSessionStateForStack(stackId: string): void {
  if (!browser) return;
  const normalizedStackId = stackId.trim();
  if (!normalizedStackId) return;

  const storage = window.sessionStorage;
  const jobIds = new Set<string>();
  const keysToRemove = new Set<string>();
  const addServerPrefix = `creating:add-server:${encodeURIComponent(normalizedStackId)}:`;
  const globalStackMatches =
    storage.getItem("creatingStackId") === normalizedStackId;

  for (let index = 0; index < storage.length; index += 1) {
    const key = storage.key(index);
    if (!key) continue;
    const match = key.match(/^creating:([^:]+):stackId$/);
    if (match && storage.getItem(key) === normalizedStackId) {
      jobIds.add(match[1]);
    }
  }

  for (let index = 0; index < storage.length; index += 1) {
    const key = storage.key(index);
    if (!key) continue;
    if (key.startsWith(addServerPrefix)) {
      keysToRemove.add(key);
      continue;
    }
    if (globalStackMatches && CREATING_SESSION_KEYS.has(key)) {
      keysToRemove.add(key);
      continue;
    }
    if (
      storage.getItem(key) === normalizedStackId &&
      key.startsWith("creating:")
    ) {
      keysToRemove.add(key);
      continue;
    }
    for (const jobId of jobIds) {
      if (key.startsWith(`creating:${jobId}:`)) {
        keysToRemove.add(key);
      }
    }
  }

  for (const key of keysToRemove) {
    storage.removeItem(key);
  }
}
