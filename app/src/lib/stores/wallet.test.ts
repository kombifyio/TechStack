import { get } from "svelte/store";
import { beforeEach, describe, expect, it } from "vitest";
import { walletStore } from "./wallet";

describe("walletStore", () => {
  beforeEach(() => {
    walletStore.reset();
  });

  it("keeps load compatibility without reading PocketBase wallet records", async () => {
    const result = await walletStore.load({ search: "owner" });

    expect(result.success).toBe(true);
    expect(result.data).toEqual([]);
    expect(get(walletStore).items).toEqual([]);
  });

  it("does not write PocketBase wallet records for legacy mutations", async () => {
    const result = await walletStore.create({
      name: "Owner",
      kind: "password",
    });

    expect(result.success).toBe(false);
    expect(result.error).toContain("backend wallet APIs");
  });

  it("keeps subscription compatibility without PocketBase realtime", () => {
    const unsubscribe = walletStore.startSubscription();

    expect(get(walletStore).subscribed).toBe(true);
    unsubscribe();
    expect(get(walletStore).subscribed).toBe(false);
  });
});
