/**
 * Last-known values for page sections, so a revisit paints before the network
 * answers.
 *
 * Same contract as `resource.svelte.ts` and deliberately the same store: this
 * is the plain-function edge of it, for pages that already own their state and
 * only need the cache half. A cached value is a RENDER ACCELERATOR and never
 * an authority — the caller must revalidate on every use and overwrite with
 * whatever comes back.
 *
 * sessionStorage rather than localStorage: this is inventory belonging to the
 * signed-in session, and it must not survive it on a shared machine.
 */

const PREFIX = "techstack-cache:";
export const DEFAULT_MAX_AGE_MS = 5 * 60 * 1000;

export interface Entry<T> {
  value: T;
  storedAt: number;
}

/** Cached value plus when it was produced, for callers that show the age. */
export function readSectionEntry<T>(
  key: string,
  maxAgeMs: number = DEFAULT_MAX_AGE_MS,
): Entry<T> | null {
  if (typeof sessionStorage === "undefined") return null;
  try {
    const raw = sessionStorage.getItem(PREFIX + key);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Entry<T>;
    if (!parsed || typeof parsed.storedAt !== "number") return null;
    if (Date.now() - parsed.storedAt > maxAgeMs) {
      sessionStorage.removeItem(PREFIX + key);
      return null;
    }
    return parsed;
  } catch {
    // Corrupt or unreadable is simply a miss; never let the cache throw into
    // a render path.
    return null;
  }
}

export function readSectionCache<T>(
  key: string,
  maxAgeMs: number = DEFAULT_MAX_AGE_MS,
): T | null {
  return readSectionEntry<T>(key, maxAgeMs)?.value ?? null;
}

export function writeSectionCache<T>(key: string, value: T): void {
  if (typeof sessionStorage === "undefined") return;
  try {
    sessionStorage.setItem(
      PREFIX + key,
      JSON.stringify({ value, storedAt: Date.now() } satisfies Entry<T>),
    );
  } catch {
    // Quota, or private mode refusing writes: the page works uncached.
  }
}

/**
 * Drop every cached section.
 *
 * Wired into the logout cleanup path: this store holds tenant inventory, and
 * sessionStorage outlives a sign-out in the same tab. Without this, signing in
 * as a different account in that tab would paint the previous account's
 * servers for as long as the revalidation takes.
 */
export function clearSectionCache(): void {
  if (typeof sessionStorage === "undefined") return;
  try {
    const doomed: string[] = [];
    for (let index = 0; index < sessionStorage.length; index += 1) {
      const key = sessionStorage.key(index);
      if (key?.startsWith(PREFIX)) doomed.push(key);
    }
    for (const key of doomed) sessionStorage.removeItem(key);
  } catch {
    // Nothing we can reach to clean up.
  }
}
