import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { getMonitoringCockpit } from "./monitoring";

describe("monitoring api", () => {
  const originalFetch = globalThis.fetch;
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            data: {
              stacks: [{ id: "stack-1", name: "Demo" }],
              techstack_id: "techstack-1",
              stack: { id: "stack-1", name: "Demo" },
              readiness: { status: "ready" },
              nextSteps: [],
              kpis: {
                registered_servers: 2,
                healthy_servers: 1,
                running_services: 3,
                active_alerts: 0,
              },
              servers: [{ id: "node-1", hostname: "node-1" }],
              services: [{ id: "svc-1", target_server_id: "node-1" }],
              monitoring: { status: "ok" },
              alerts: [],
              jobs: [{ id: "job-1", state: "completed", progress: 100 }],
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      ),
    );
    globalThis.fetch = fetchMock as unknown as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("loads the stack-scoped monitoring cockpit BFF", async () => {
    const result = await getMonitoringCockpit("techstack-1");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining(
        "/api/v1/monitor/cockpit?techstack_id=techstack-1",
      ),
      expect.objectContaining({ credentials: "include" }),
    );
    expect(result.techstack_id).toBe("techstack-1");
    expect(result.kpis.registered_servers).toBe(2);
    expect(result.services[0].target_server_id).toBe("node-1");
    expect(result.jobs[0].id).toBe("job-1");
  });
});
