import { describe, expect, it, beforeAll, beforeEach } from "vitest";
import { resource } from "./resource.svelte.js";
import { clearSectionCache } from "./sectionCache.js";

/*
 * The cache path only runs where sessionStorage exists, and this suite runs
 * in node. A Map-backed stand-in keeps the test honest — it exercises the
 * real read/write/expiry code rather than asserting around it.
 */
beforeAll(() => {
  const store = new Map<string, string>();
  Object.defineProperty(globalThis, "sessionStorage", {
    configurable: true,
    value: {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
      clear: () => store.clear(),
      key: (i: number) => [...store.keys()][i] ?? null,
      get length() {
        return store.size;
      },
    },
  });
});

/*
 * Behaviour, not shape: these cover the three things the helper exists for —
 * a section publishes independently, a revisit paints from cache, and a
 * failed refresh does not blank what was already on screen.
 */

beforeEach(() => {
  clearSectionCache();
});

describe("resource", () => {
  it("publishes as soon as its own request settles", async () => {
    const fast = resource(async () => "quick");
    const slow = resource(
      () => new Promise<string>((done) => setTimeout(() => done("late"), 50)),
    );

    const slowDone = slow.refresh();
    await fast.refresh();

    // The point of the helper: the fast section is readable while the slow
    // one is still in flight.
    expect(fast.data).toBe("quick");
    expect(slow.data).toBeUndefined();
    expect(slow.loading).toBe(true);

    await slowDone;
    expect(slow.data).toBe("late");
  });

  it("paints last-known data on the next visit, then revalidates", async () => {
    const first = resource(async () => ({ count: 1 }), { key: "servers" });
    await first.refresh();

    // A fresh instance stands in for navigating back to the page.
    const revisit = resource(async () => ({ count: 2 }), { key: "servers" });
    expect(revisit.data).toEqual({ count: 1 });
    expect(revisit.stale).toBe(true);

    await revisit.refresh();
    expect(revisit.data).toEqual({ count: 2 });
    expect(revisit.stale).toBe(false);
  });

  it("keeps showing the old value when a refresh fails", async () => {
    let attempt = 0;
    const r = resource(async () => {
      attempt += 1;
      if (attempt === 2) throw new Error("backend down");
      return attempt;
    });

    await r.refresh();
    expect(r.data).toBe(1);

    await r.refresh();
    expect(r.error).toBeInstanceOf(Error);
    expect(r.data).toBe(1);
  });

  it("ignores a slow response that lost the race to a newer one", async () => {
    let call = 0;
    const r = resource(async () => {
      call += 1;
      const mine = call;
      await new Promise((done) => setTimeout(done, mine === 1 ? 40 : 0));
      return mine;
    });

    const stale = r.refresh();
    const fresh = r.refresh();
    await Promise.all([stale, fresh]);

    expect(r.data).toBe(2);
  });

  it("fails a request that outruns its timeout", async () => {
    const r = resource(
      () => new Promise<string>((done) => setTimeout(() => done("never"), 80)),
      { timeoutMs: 10 },
    );

    await r.refresh();
    expect(r.error).toBeInstanceOf(Error);
    expect(r.loading).toBe(false);
  });
});
