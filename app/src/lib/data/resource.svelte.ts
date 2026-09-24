/**
 * A page-section's data, with a cache — so a screen fills in piece by piece
 * and a revisit is not a blank wait.
 *
 * Two problems this exists for, both measured on the live dashboard:
 *
 * 1. The page fetched its three resources with `Promise.allSettled` and then
 *    assigned every result at once. The requests were parallel; the RENDER
 *    was not. A workers list that came back in 200 ms still waited for the
 *    slowest call — up to the 8 s timeout — before anything appeared.
 * 2. Nothing was cached, so returning to a page you had just left showed the
 *    same empty skeletons all over again.
 *
 * So each section owns its own resource: it publishes the moment ITS request
 * settles, independent of its neighbours, and it paints last-known data
 * immediately while revalidating behind it (stale-while-revalidate). A stale
 * read is marked as such, so a section can show a quiet refresh hint instead
 * of pretending the number is live.
 *
 * Cache lives in sessionStorage, not localStorage: this is inventory for the
 * signed-in session and must not outlive it on a shared machine. It is a
 * render accelerator and never an authority — every entry is revalidated on
 * use, and a rejected revalidation surfaces as an error rather than leaving
 * the stale value looking fresh.
 */

import {
  DEFAULT_MAX_AGE_MS,
  clearSectionCache,
  readSectionEntry,
  writeSectionCache,
} from "./sectionCache.js";

/* One store, one owner: `sectionCache` holds the keys, the age rule and the
 * clearing path, so the logout cleanup that drops it cannot miss half of it. */
export { clearSectionCache };

export interface Resource<T> {
  /** Last known value: cached on first paint, fresh once loaded. */
  readonly data: T | undefined;
  /** A request is in flight. With cached data present this means refreshing. */
  readonly loading: boolean;
  /** The shown value came from cache and has not been confirmed yet. */
  readonly stale: boolean;
  /** The last failure. Cached data stays visible next to it. */
  readonly error: unknown;
  /** When the shown value was produced. */
  readonly storedAt: number | undefined;
  /** Fetch and publish. Safe to call repeatedly; the newest call wins. */
  refresh(): Promise<T | undefined>;
}

export interface ResourceOptions {
  /** Cache key. Omit to skip caching entirely. */
  key?: string;
  maxAgeMs?: number;
  /** Reject slow requests so a hung backend cannot pin a section forever. */
  timeoutMs?: number;
}

export function resource<T>(
  fetcher: () => Promise<T>,
  options: ResourceOptions = {},
): Resource<T> {
  const { key, maxAgeMs = DEFAULT_MAX_AGE_MS, timeoutMs } = options;

  const cached = key ? readSectionEntry<T>(key, maxAgeMs) : null;

  let data = $state<T | undefined>(cached?.value);
  let loading = $state(false);
  let stale = $state(cached !== null);
  let error = $state<unknown>(null);
  let storedAt = $state<number | undefined>(cached?.storedAt);

  /* Out-of-order responses must not win: a slow first call landing after a
   * fast second one would show older data than the user already saw. */
  let generation = 0;

  async function refresh(): Promise<T | undefined> {
    const mine = ++generation;
    loading = true;
    error = null;

    try {
      const value = await (timeoutMs
        ? Promise.race([
            fetcher(),
            new Promise<never>((_, reject) =>
              setTimeout(
                () => reject(new Error(`${key ?? "resource"} timed out`)),
                timeoutMs,
              ),
            ),
          ])
        : fetcher());

      if (mine !== generation) return data;

      data = value;
      stale = false;
      storedAt = Date.now();
      if (key) writeSectionCache(key, value);
      return value;
    } catch (err) {
      if (mine !== generation) return data;
      error = err;
      // Deliberately keeping `data`: a failed refresh should not blank a
      // section that was showing something valid a moment ago.
      return data;
    } finally {
      if (mine === generation) loading = false;
    }
  }

  return {
    get data() {
      return data;
    },
    get loading() {
      return loading;
    },
    get stale() {
      return stale;
    },
    get error() {
      return error;
    },
    get storedAt() {
      return storedAt;
    },
    refresh,
  };
}
