import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  getManagedServiceLogs,
  listCanonicalServices,
  runManagedServiceAction,
} from "./services";

function requestPath(input: unknown): string {
  const raw = input instanceof Request ? input.url : String(input);
  return new URL(raw, "https://techstack.test").pathname;
}

describe("managed service API client", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.stubGlobal("window", {
      location: { origin: "https://techstack.test" },
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("scopes the central canonical service read by Techstack", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ data: [] }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );

    await listCanonicalServices("techstack-1");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining(
        "/api/v1/services?techstack_id=techstack-1",
      ),
      expect.objectContaining({ credentials: "include" }),
    );
  });

  it("queues a service action with the inventory revision and idempotency key", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ token: "csrf-token" }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            data: {
              job_id: "job-restart-1",
              service_id: "service-1",
              action: "restart",
              status: "queued",
            },
          }),
          { status: 202, headers: { "content-type": "application/json" } },
        ),
      );

    const result = await runManagedServiceAction(
      "service-1",
      "restart",
      7,
      "idempotency-1",
    );

    expect(result).toMatchObject({
      job_id: "job-restart-1",
      action: "restart",
      status: "queued",
    });
    const actionCall = fetchMock.mock.calls.find(
      ([input]) =>
        requestPath(input) === "/api/v1/registry/services/service-1/actions",
    );
    expect(actionCall).toBeDefined();
    const [, options] = actionCall!;
    expect(options).toMatchObject({
      method: "POST",
      headers: expect.any(Headers),
    });
    expect((options?.headers as Headers).get("Idempotency-Key")).toBe(
      "idempotency-1",
    );
    expect(JSON.parse(String(options?.body))).toEqual({
      action: "restart",
      expected_inventory_revision: 7,
      owner_approved: true,
    });
  });

  it("requests an exact bounded, cursor-backed log page", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          data: {
            service_id: "service-1",
            job_id: "job-logs-1",
            status: "completed",
            entries: [{ timestamp: "2026-08-11T10:00:00Z", message: "ready" }],
            next_cursor: "older-page",
          },
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    const page = await getManagedServiceLogs("service-1", {
      limit: 25,
      cursor: "page-2",
    });

    expect(page.entries).toEqual([
      { timestamp: "2026-08-11T10:00:00Z", message: "ready" },
    ]);
    expect(page.next_cursor).toBe("older-page");
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringMatching(
        /\/api\/v1\/registry\/services\/service-1\/logs\?limit=25&cursor=page-2$/,
      ),
      expect.objectContaining({ credentials: "include" }),
    );
  });

  it("rejects an unbounded log request before it reaches the API", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch");

    await expect(
      getManagedServiceLogs("service-1", { limit: 201 }),
    ).rejects.toThrow("Service log limit must be an integer between 1 and 200");
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
