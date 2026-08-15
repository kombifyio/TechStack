import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  addManagedRuntimeServer,
  assignStackWorker,
  createStack,
  decommissionMonthlyRuntime,
  deployStack,
  exportKombinationSpec,
  forceDecommissionMonthlyRuntime,
  getStack,
  getMonthlyRuntimeOfferings,
  getMonthlyRuntimeOperations,
  getMonthlyRuntimeStatus,
  importKombinationSpec,
  validateKombinationImport,
  pruneOrphanStacks,
  provisionStack,
  reconnectMonthlyRuntime,
  resolveMonthlyRuntimeCustody,
  resumeStackEnrollment,
  retryStackRollout,
  startMonthlyRuntime,
} from "./stacks";

describe("stacks api", () => {
  const originalFetch = globalThis.fetch;
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/api/v1/monthly-runtimes/offerings")) {
        return new Response(
          JSON.stringify({
            data: [
              {
                id: "standard",
                name: "Monthly Runtime Standard",
                billing_cadence: "monthly",
              },
            ],
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      }
      if (url.includes("/api/v1/monthly-runtimes/lease-123")) {
        if (url.includes("/operations")) {
          return new Response(
            JSON.stringify({
              data: [
                {
                  tenant_id: "org-1",
                  lease_id: "lease-123",
                  event_type: "runtime_action",
                  status: "ssh_enabled",
                  actor: "user-1",
                  created_at: "2026-05-13T12:00:00Z",
                },
              ],
            }),
            { status: 200, headers: { "content-type": "application/json" } },
          );
        }
        return new Response(
          JSON.stringify({
            data: {
              tenant_id: "org-1",
              lease_id: "lease-123",
              action: init?.method === "POST" ? "start" : "status",
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      }
      if (url.match(/\/api\/v1\/stacks\/stack-123$/)) {
        return new Response(
          JSON.stringify({
            data: {
              id: "stack-123",
              name: "Demo Stack",
              provider: "local",
              state: "running",
              services: ["files"],
              stackkit_catalog_ref: "cloud-kit",
              created_at: "2026-05-13T00:00:00Z",
              updated_at: "2026-05-13T00:00:00Z",
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      }
      if (url.includes("/api/v1/stacks/stack-123/export")) {
        return new Response(
          JSON.stringify({
            stack_spec: {
              name: "configured-stack",
              stackkit: "basement-kit",
              services: { homepage: { enabled: true } },
            },
            stack_id: "stack-123",
            exported_at: "2026-05-13T00:00:00Z",
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      }
      if (url.includes("/api/v1/stacks/import/validate")) {
        return new Response(
          JSON.stringify({
            data: {
              valid: true,
              warnings: [{ path: "services", code: "preview", message: "ok" }],
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      }
      if (url.includes("/api/v1/stacks/import")) {
        return new Response(
          JSON.stringify({
            data: {
              stack_id: "stack-imported",
              job_id: "job-imported",
              name: "Imported Stack",
              state: "provisioning",
              message: "Stack created; Unifier started",
            },
          }),
          { status: 202, headers: { "content-type": "application/json" } },
        );
      }
      if (url.includes("/api/v1/stacks/stack-123/workers/worker-1/assign")) {
        return new Response(
          JSON.stringify({
            data: {
              stack_id: "stack-123",
              worker_id: "worker-1",
              server: {
                id: "worker-1",
                hostname: "node-1",
                role: "worker",
                status: "healthy",
                assignment: "stack",
                agent_id: "agent-1",
                approved: true,
                precheck_state: "passed",
                capabilities: {},
                health: {
                  state: "healthy",
                  source: "worker-registry",
                  cpu_percent: { status: "unknown", unit: "%" },
                  memory_percent: { status: "unknown", unit: "%" },
                  disk_percent: { status: "unknown", unit: "%" },
                  uptime_seconds: { status: "unknown", unit: "s" },
                },
              },
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      }
      if (url.includes("/api/v1/stacks/stack-123/managed-runtimes")) {
        return new Response(
          JSON.stringify({
            data: {
              stack_id: "stack-123",
              job_id: "job-native-1",
              lease_id: "lease-stack-123-worker-abcd1234",
              runtime_server_id: "server-native-1",
              resource_generation_id: "11111111-1111-4111-8111-111111111111",
              operation_id: "operation-native-1",
              provider_id: "ionos",
              node_role: "worker",
              runtime_offering_id: "monthly-runtime-standard",
              enrollment_status: "pending",
              runtime_phase: "lease_pending",
              idempotent_replay: false,
              message:
                "Native managed runtime admission accepted; provisioning is pending",
            },
          }),
          { status: 202, headers: { "content-type": "application/json" } },
        );
      }
      if (url.includes("/api/v1/stacks/prune-orphans")) {
        return new Response(
          JSON.stringify({
            data: {
              message: "Orphan stacks pruned",
              pruned_stacks: 2,
              skipped_active: 1,
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      }
      return new Response(
        JSON.stringify({
          success: true,
          message: "Deployment started",
          job_id: "job-123",
        }),
        {
          status: 202,
          headers: { "content-type": "application/json" },
        },
      );
    });
    globalThis.fetch = fetchMock as unknown as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.useRealTimers();
  });

  it("starts rollout through the deploy endpoint, not provision", async () => {
    const result = await deployStack("stack-123");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/stacks/stack-123/deploy"),
      expect.objectContaining({ method: "POST" }),
    );
    expect(String(fetchMock.mock.calls[0][0])).not.toContain("/provision");
    expect(result.job_id).toBe("job-123");
  });

  it("restarts provisioning from the persisted Wizard stack", async () => {
    const result = await provisionStack("stack-123");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/stacks/stack-123/provision"),
      expect.objectContaining({ method: "POST" }),
    );
    expect(result.job_id).toBe("job-123");
  });

  it("resumes an enrollment wait with the exact source job and lease", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            success: true,
            message: "Enrollment rollout recovery accepted",
            stack_id: "stack-123",
            job_id: "job-enrollment-replacement",
            source_job_id: "job-waiting",
            lease_id: "lease-existing",
            server_id: "server-existing",
            idempotent_replay: false,
            provider_vm_create_requested: false,
          },
        }),
        { status: 202, headers: { "content-type": "application/json" } },
      ),
    );
    const result = await resumeStackEnrollment("stack-123", {
      job_id: "job-waiting",
      lease_id: "lease-existing",
    });

    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toContain("/api/v1/stacks/stack-123/resume-enrollment");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({
      job_id: "job-waiting",
      lease_id: "lease-existing",
    });
    expect(String(url)).not.toContain("/deploy");
    expect(String(url)).not.toContain("monthly-runtimes");
    expect(result).toMatchObject({
      job_id: "job-enrollment-replacement",
      source_job_id: "job-waiting",
      lease_id: "lease-existing",
      server_id: "server-existing",
      provider_vm_create_requested: false,
    });
  });

  it("retries a failed rollout through the exact agent-native endpoint", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            success: true,
            message: "Exact rollout retry accepted",
            stack_id: "stack-123",
            job_id: "job-rollout-replacement",
            source_job_id: "job-failed",
            lease_id: "lease-existing",
            server_id: "server-existing",
            idempotent_replay: false,
            provider_vm_create_requested: false,
          },
        }),
        { status: 202, headers: { "content-type": "application/json" } },
      ),
    );
    const result = await retryStackRollout("stack-123", {
      source_job_id: "job-failed",
      lease_id: "lease-existing",
    });
    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toContain("/api/v1/stacks/stack-123/retry-rollout");
    expect(String(url)).not.toContain("/deploy");
    expect(JSON.parse(String(init?.body))).toEqual({
      source_job_id: "job-failed",
      lease_id: "lease-existing",
    });
    expect(result.provider_vm_create_requested).toBe(false);
  });

  it("sends the wizard idempotency key on stack creation", async () => {
    await createStack({ name: "Demo", services: [] }, "wizard-attempt-1");

    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toContain("/api/v1/stacks");
    expect(init?.method).toBe("POST");
    expect(new Headers(init?.headers).get("X-Idempotency-Key")).toBe(
      "wizard-attempt-1",
    );
  });

  it("loads one stack through the canonical stack detail endpoint", async () => {
    const result = await getStack("stack-123");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/stacks/stack-123"),
      expect.objectContaining({ method: "GET" }),
    );
    expect(result.id).toBe("stack-123");
    expect(result.stackkit_catalog_ref).toBe("cloud-kit");
  });

  it("loads provider-free monthly runtime offerings", async () => {
    const result = await getMonthlyRuntimeOfferings();

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/monthly-runtimes/offerings"),
      expect.objectContaining({ method: "GET" }),
    );
    expect(JSON.stringify(result).toLowerCase()).not.toContain("provider");
    expect(result[0].id).toBe("standard");
  });

  it("calls monthly runtime status and action endpoints by lease id", async () => {
    await getMonthlyRuntimeStatus("lease-123", "org-1");
    await startMonthlyRuntime("lease-123", "org-1");
    await decommissionMonthlyRuntime("lease-123", "org-1");
    await reconnectMonthlyRuntime("lease-123", "org-1");

    expect(String(fetchMock.mock.calls[0][0])).toContain(
      "/api/v1/monthly-runtimes/lease-123?tenant_id=org-1",
    );
    expect(fetchMock.mock.calls[1]).toEqual([
      expect.stringContaining(
        "/api/v1/monthly-runtimes/lease-123/start?tenant_id=org-1",
      ),
      expect.objectContaining({ method: "POST" }),
    ]);
    expect(fetchMock.mock.calls[2]).toEqual([
      expect.stringContaining(
        "/api/v1/monthly-runtimes/lease-123/decommission?tenant_id=org-1",
      ),
      expect.objectContaining({ method: "POST" }),
    ]);
    expect(fetchMock.mock.calls[3]).toEqual([
      expect.stringContaining(
        "/api/v1/monthly-runtimes/lease-123/reconnect?tenant_id=org-1",
      ),
      expect.objectContaining({ method: "POST" }),
    ]);
  });

  it("force decommissions monthly runtime with an explicit force body", async () => {
    await forceDecommissionMonthlyRuntime("lease-123", "org-1");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining(
        "/api/v1/monthly-runtimes/lease-123/decommission?tenant_id=org-1",
      ),
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ force: true }),
      }),
    );
  });

  it("resolves legacy custody only with an explicit provider-cleanup confirmation", async () => {
    await resolveMonthlyRuntimeCustody("lease-legacy", "org-1");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining(
        "/api/v1/monthly-runtimes/lease-legacy/resolve-custody?tenant_id=org-1",
      ),
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ provider_cleanup_confirmed: true }),
      }),
    );
  });

  it("keeps decommission requests alive beyond the default short timeout", async () => {
    vi.useFakeTimers();
    fetchMock.mockImplementationOnce(
      (_input: RequestInfo | URL, init?: RequestInit) =>
        new Promise<Response>((_resolve, reject) => {
          const signal = init?.signal;
          signal?.addEventListener("abort", () => {
            const err = new Error("aborted");
            err.name = "AbortError";
            reject(err);
          });
        }),
    );

    let settled = false;
    const result = decommissionMonthlyRuntime("lease-timeout").then(
      () => {
        settled = true;
        return "";
      },
      (err: unknown) => {
        settled = true;
        return err instanceof Error ? err.message : String(err);
      },
    );

    await vi.advanceTimersByTimeAsync(10_000);
    expect(settled).toBe(false);

    await vi.advanceTimersByTimeAsync(180_000);
    await expect(result).resolves.toContain("Request timed out after 190s");
    expect(settled).toBe(true);
  });

  it("loads monthly runtime operation history by lease id", async () => {
    const result = await getMonthlyRuntimeOperations("lease-123", "org-1", 25);

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining(
        "/api/v1/monthly-runtimes/lease-123/operations?tenant_id=org-1&limit=25",
      ),
      expect.objectContaining({ method: "GET" }),
    );
    expect(result[0]).toMatchObject({
      event_type: "runtime_action",
      status: "ssh_enabled",
      actor: "user-1",
    });
  });

  it("reads canonical stack_spec from JSON export responses", async () => {
    const result = await exportKombinationSpec("stack-123", "json");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/stacks/stack-123/export"),
      expect.objectContaining({
        method: "GET",
        headers: expect.objectContaining({ Accept: "application/json" }),
      }),
    );
    expect(result.content).toContain('"stackkit": "basement-kit"');
    expect(result.content).toContain('"homepage"');
  });

  it("unwraps validation and import envelopes for stack-spec import", async () => {
    const validation = await validateKombinationImport("name: imported");
    const result = await importKombinationSpec("name: imported");

    expect(validation.valid).toBe(true);
    expect(validation.warnings?.[0].code).toBe("preview");
    expect(result.stack_id).toBe("stack-imported");
    expect(result.job_id).toBe("job-imported");
  });

  it("accepts raw validation and import payloads during import compatibility", async () => {
    fetchMock
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ valid: true }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            stack_id: "stack-raw",
            job_id: "job-raw",
            name: "Raw Stack",
            state: "provisioning",
            message: "created",
          }),
          { status: 202, headers: { "content-type": "application/json" } },
        ),
      );

    const validation = await validateKombinationImport("name: raw");
    const result = await importKombinationSpec("name: raw");

    expect(validation.valid).toBe(true);
    expect(result.stack_id).toBe("stack-raw");
    expect(result.job_id).toBe("job-raw");
  });

  it("assigns an approved worker to the selected stack", async () => {
    const result = await assignStackWorker("stack-123", "worker-1");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining(
        "/api/v1/stacks/stack-123/workers/worker-1/assign",
      ),
      expect.objectContaining({ method: "POST" }),
    );
    expect(result.server.assignment).toBe("stack");
  });

  it("prunes only orphan stacks through the lease-safe endpoint", async () => {
    const result = await pruneOrphanStacks();

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/stacks/prune-orphans"),
      expect.objectContaining({ method: "POST" }),
    );
    expect(result.pruned_stacks).toBe(2);
    expect(result.skipped_active).toBe(1);
  });

  it("targets one legacy dead card without destroying infrastructure", async () => {
    await pruneOrphanStacks("legacy stack/1");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining(
        "/api/v1/stacks/prune-orphans?stack_id=legacy%20stack%2F1",
      ),
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("adds an additional managed runtime server to an existing stack", async () => {
    const result = await addManagedRuntimeServer(
      "stack-123",
      {
        node_role: "worker",
        provider_id: "ionos",
        ionos_datacenter: "us/ewr",
        provider_region: "us/ewr",
        runtime_offering_id: "monthly-runtime-standard",
        services: ["files"],
      },
      "opaque-add-server-key",
    );

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/stacks/stack-123/managed-runtimes"),
      expect.objectContaining({
        method: "POST",
        body: expect.stringContaining('"node_role":"worker"'),
      }),
    );
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/stacks/stack-123/managed-runtimes"),
      expect.objectContaining({
        body: expect.stringContaining('"provider_id":"ionos"'),
      }),
    );
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/stacks/stack-123/managed-runtimes"),
      expect.objectContaining({
        body: expect.stringContaining('"ionos_datacenter":"us/ewr"'),
      }),
    );
    const managedRuntimeCall = fetchMock.mock.calls.find(([input]) =>
      String(input).includes("/api/v1/stacks/stack-123/managed-runtimes"),
    );
    expect(managedRuntimeCall).toBeDefined();
    const managedRuntimeInit = managedRuntimeCall?.[1] as RequestInit;
    expect(new Headers(managedRuntimeInit.headers).get("Idempotency-Key")).toBe(
      "opaque-add-server-key",
    );
    expect(String(managedRuntimeInit.body)).not.toContain(
      "opaque-add-server-key",
    );
    expect(result.lease_id).toBe("lease-stack-123-worker-abcd1234");
  });
});
