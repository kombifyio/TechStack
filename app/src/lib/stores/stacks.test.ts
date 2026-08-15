import { get } from "svelte/store";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createStack, listStacks } from "$lib/api/stacks";
import { stacksStore } from "./stacks";

vi.mock("$lib/api/stacks", () => ({
  createStack: vi.fn(),
  listStacks: vi.fn(),
}));

const listStacksMock = vi.mocked(listStacks);
const createStackMock = vi.mocked(createStack);

describe("stacksStore", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    stacksStore.reset();
  });

  it("loads stacks through the v1 stacks API and filters in store state", async () => {
    listStacksMock.mockResolvedValue([
      {
        id: "stack-1",
        name: "Media",
        provider: "local",
        state: "running",
        services: ["jellyfin"],
        runtime_phase: "day2",
        created_at: "2026-05-28T10:00:00Z",
        updated_at: "2026-05-28T10:01:00Z",
      },
      {
        id: "stack-2",
        name: "Labs",
        provider: "local",
        state: "draft",
        services: [],
        created_at: "2026-05-28T11:00:00Z",
        updated_at: "2026-05-28T11:01:00Z",
      },
    ]);

    const result = await stacksStore.load({
      status: "running",
      search: "media",
    });

    expect(result.success).toBe(true);
    expect(listStacksMock).toHaveBeenCalledTimes(1);
    expect(get(stacksStore).items).toHaveLength(1);
    expect(get(stacksStore).items[0]).toMatchObject({
      id: "stack-1",
      collectionName: "stacks",
      name: "Media",
      status: "running",
      services: ["jellyfin"],
      nodeCount: 0,
      serviceCount: 0,
    });
  });

  it("creates stacks through the v1 stack creation API", async () => {
    createStackMock.mockResolvedValue({
      stack_id: "stack-new",
      job_id: "job-new",
      name: "New Stack",
      state: "pending",
      message: "queued",
    });

    const result = await stacksStore.create({
      name: "New Stack",
      mode: "techie",
      services: ["postgres"],
      config: { region: "eu-central" },
    });

    expect(result.success).toBe(true);
    expect(createStackMock).toHaveBeenCalledWith({
      name: "New Stack",
      mode: "techie",
      services: ["postgres"],
      user_config: { region: "eu-central" },
    });
    expect(get(stacksStore).items[0]).toMatchObject({
      id: "stack-new",
      name: "New Stack",
      status: "pending",
      mode: "techie",
    });
  });

  it("does not write PocketBase stack records for unsupported legacy mutations", async () => {
    const result = await stacksStore.update("stack-1", { name: "Changed" });

    expect(result.success).toBe(false);
    expect(result.error).toContain("backend stack APIs");
    expect(listStacksMock).not.toHaveBeenCalled();
    expect(createStackMock).not.toHaveBeenCalled();
  });

  it("keeps subscription compatibility without PocketBase realtime", async () => {
    listStacksMock.mockResolvedValue([]);

    const unsubscribe = stacksStore.startSubscription();

    expect(get(stacksStore).subscribed).toBe(true);
    await vi.waitFor(() => {
      expect(listStacksMock).toHaveBeenCalledTimes(1);
    });

    unsubscribe();
    expect(get(stacksStore).subscribed).toBe(false);
  });
});
