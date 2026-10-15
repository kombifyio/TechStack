import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  addManagedRuntimeServer,
  assignStackWorker,
  decommissionMonthlyRuntime,
  deployStack,
  exportKombinationSpec,
  forceDecommissionMonthlyRuntime,
  getKitDeployment,
  getMonthlyRuntimeOfferings,
  getMonthlyRuntimeOperations,
  getMonthlyRuntimeStatus,
  importKombinationSpec,
  validateKombinationImport,
  applyOrphanCleanup,
  planOrphanCleanup,
  provisionStack,
  reconnectMonthlyRuntime,
  resolveMonthlyRuntimeCustody,
  runStackKitLifecycleOperation,
  stackKitRecoveryPoints,
  stackKitRestoreWasStaged,
  stackKitStagedRestore,
  stackKitRestoreAdmission,
  stackKitRestoreOutcome,
  stackKitRestoreRequestDigest,
  stackKitRestoreNotAdmitted,
  resumeStackEnrollment,
  retryStackRollout,
} from "./stacks";

describe("stacks api", () => {
  const originalFetch = globalThis.fetch;
  let fetchMock: ReturnType<typeof vi.fn>;

  it("selects only the current node's valid recorded points and requires matching staged evidence", () => {
    const node = "node-history-fixture";
    const stack = "stack-history-fixture";
    const older = `sha256:${"a".repeat(64)}`;
    const newer = `sha256:${"b".repeat(64)}`;
    const status = {
      schema_version: "techstack.stackkit-lifecycle-result/v1",
      techstack_id: stack,
      operation: "backup_status",
      agent_id: node,
      success: true,
      command_result: {
        schemaVersion: "stackkit.command-result/v1",
        command: "stackkit backup status",
        status: "success",
        data: {
          history: {
            snapshot: { evidenceId: newer },
            recoveryPoints: [
              {
                evidenceId: newer,
                recordedAt: "2026-10-08T12:00:00Z",
                currentPlan: true,
                state: "recorded",
              },
              {
                evidenceId: "--untrusted",
                recordedAt: "2026-10-07T12:00:00Z",
                currentPlan: false,
                state: "recorded",
              },
              {
                evidenceId: older,
                recordedAt: "2026-10-07T12:00:00Z",
                currentPlan: false,
                state: "recorded",
              },
            ],
          },
        },
      },
    };
    const selected = stackKitRecoveryPoints(status, node, stack).find(
      (point) => !point.currentPlan,
    );
    expect(selected?.evidenceId).toBe(older);
    expect(
      stackKitRecoveryPoints(status, "another-node", stack).find(
        (point) => point.evidenceId === older,
      ),
    ).toBeUndefined();
    expect(
      stackKitRecoveryPoints(status, node, "another-stack").find(
        (point) => point.evidenceId === older,
      ),
    ).toBeUndefined();
    const legacy = structuredClone(status);
    delete (legacy.command_result.data.history as { recoveryPoints?: unknown })
      .recoveryPoints;
    expect(
      stackKitRecoveryPoints(legacy, node, stack).find(
        (point) => point.evidenceId === newer,
      ),
    ).toBeUndefined();
    const completed = {
      schema_version: "techstack.stackkit-lifecycle-result/v1",
      techstack_id: stack,
      operation: "backup_restore",
      agent_id: node,
      success: true,
    };
    expect(stackKitRestoreWasStaged(completed, node, older, stack)).toBe(false);
    const staged = {
      ...completed,
      stackkit_instance_id: "home",
      backup: {
        mode: "staged",
        snapshot_anchor_id: older,
        restore_result_id: `sha256:${"c".repeat(64)}`,
        plan_hash: `sha256:${"d".repeat(64)}`,
        owner_ref: "owner/node",
      },
    };
    expect(
      stackKitRestoreWasStaged(staged, node, selected!.evidenceId, stack),
    ).toBe(true);
    expect(stackKitRestoreWasStaged(staged, node, newer, stack)).toBe(false);
    expect(stackKitRestoreWasStaged(staged, "another-node", older, stack)).toBe(
      false,
    );
    expect(stackKitRestoreWasStaged(staged, node, older, "another-stack")).toBe(
      false,
    );
    const binding = {
      ...stackKitStagedRestore(staged, node, older, stack)!,
      stackId: stack,
      agentId: node,
      nodeId: "server-fixture",
    };
    const admissionData = {
      stack_id: stack,
      agent_id: node,
      node_id: binding.nodeId,
      stackkit_instance_id: "home",
      restore_result_id: binding.restoreResultId,
      activation_operation_id: "restore-job-original",
      plan_hash: binding.planHash,
    };
    const request = {
      schema_version: "techstack.restore-activation-request/v1",
      tenant_id: "tenant-local",
      owner_id: "local-owner",
      techstack_id: stack,
      agent_id: node,
      node_id: binding.nodeId,
      stackkit_instance_id: "home",
      restore_result_id: binding.restoreResultId,
      owner_approved: true,
      key_sha256: "a".repeat(64),
      request_digest: "e".repeat(64),
    };
    const result = {
      ...staged,
      operation: "backup_restore_activate",
      success: false,
      restore_activation_admission: admissionData,
      restore_activation_request: request,
      backup: {
        mode: "recovered",
        restore_result_id: binding.restoreResultId,
        activation_operation_id: admissionData.activation_operation_id,
        plan_hash: binding.planHash,
      },
      command_result: {
        schemaVersion: "stackkit.command-result/v1",
        command: "stackkit backup restore recover",
        status: "success",
        data: {
          apiVersion: "stackkit.restore-activation-result/v1",
          status: "recovered",
          operationId: admissionData.activation_operation_id,
          restoreResultId: binding.restoreResultId,
          planHash: binding.planHash,
          safetySnapshotId: older,
          managedVolumeSetHash: newer,
          verification: {
            apiVersion: "stackkit.local-backup-restore-verification/v1",
            ownerRef: "owner/node",
            ownerBindingDigest: older,
            pocketIdSubject: "local-owner",
            planHash: binding.planHash,
            servicesVerified: true,
            verifiedAt: "2026-10-09T01:00:00Z",
          },
          signature: {
            ownerRef: "owner/node",
            keyId: "owner-key",
            value: "verified-by-node",
          },
        },
      },
    };
    const admission = stackKitRestoreAdmission(result, binding)!;
    expect(
      stackKitRestoreOutcome(
        result,
        binding,
        "backup_restore_activate",
        admission,
      )?.mode,
    ).toBe("recovered");
    expect(
      stackKitRestoreRequestDigest(
        result,
        binding,
        "local-owner",
        "tenant-local",
        "a".repeat(64),
      ),
    ).toBe(request.request_digest);
    expect(
      stackKitRestoreRequestDigest(
        result,
        binding,
        "other-owner",
        "tenant-local",
        "a".repeat(64),
      ),
    ).toBeUndefined();
    expect(
      stackKitRestoreRequestDigest(
        result,
        binding,
        "local-owner",
        "tenant-local",
        "b".repeat(64),
      ),
    ).toBeUndefined();
    expect(
      stackKitRestoreAdmission(result, { ...binding, nodeId: "other-server" }),
    ).toBeUndefined();
    expect(
      stackKitRestoreOutcome(
        result,
        { ...binding, instanceId: "foreign-instance" },
        "backup_restore_activate",
        admission,
      ),
    ).toBeUndefined();
    expect(
      stackKitRestoreOutcome(
        result,
        { ...binding, ownerRef: "foreign-owner" },
        "backup_restore_activate",
        admission,
      ),
    ).toBeUndefined();
    const denied = {
      restore_activation_request: request,
      backup: {
        mode: "not-admitted",
        job_id: "job-original",
        attempt_started_at: "2026-10-09T01:00:00.123456Z",
        restore_result_id: binding.restoreResultId,
        key_sha256: request.key_sha256,
        request_digest: request.request_digest,
      },
    };
    expect(
      stackKitRestoreNotAdmitted(
        denied,
        binding,
        "local-owner",
        "tenant-local",
        request.key_sha256,
        request.request_digest,
        "job-original",
        "2026-10-09T03:00:00.123456789+02:00",
      ),
    ).toBe(true);
    expect(
      stackKitRestoreNotAdmitted(
        denied,
        binding,
        "local-owner",
        "tenant-local",
        request.key_sha256,
        request.request_digest,
        "job-original",
        "2026-10-09T01:00:00.123457Z",
      ),
    ).toBe(false);
    expect(
      stackKitRestoreNotAdmitted(
        { ...denied, restore_activation_admission: admissionData },
        binding,
        "local-owner",
        "tenant-local",
        request.key_sha256,
        request.request_digest,
        "job-original",
        "2026-10-09T01:00:00.123456Z",
      ),
    ).toBe(false);
  });

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
              kit_deployment_id: "stack-123",
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
            data: {
              stack_spec: {
                name: "configured-stack",
                stackkit: "basement-kit",
                services: { homepage: { enabled: true } },
              },
              kit_deployment_id: "stack-123",
              exported_at: "2026-05-13T00:00:00Z",
            },
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
              kit_deployment_id: "stack-imported",
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
              kit_deployment_id: "stack-123",
              worker_id: "worker-1",
              server: {
                id: "worker-1",
                hostname: "node-1",
                role: "worker",
                status: "healthy",
                assignment: "stack",
                kit_deployment_id: "stack-123",
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
              kit_deployment_id: "stack-123",
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
              mode: "dry_run",
              message: "Control-plane cleanup plan ready",
              digest: "sha256:cleanup-plan",
              candidates: [
                {
                  resource_type: "stack_projection",
                  id: "stack-123",
                  name: "e2e-stack-123",
                  class: "e2e_test_stack",
                  reason:
                    "owned_e2e_projection_without_live_lease_or_recent_worker",
                },
              ],
              applied: { stacks: 0, workers: 0, total: 0 },
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
    const result = await deployStack("stack-123", "deploy-attempt-1");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/stacks/stack-123/deploy"),
      expect.objectContaining({ method: "POST" }),
    );
    expect(String(fetchMock.mock.calls[0][0])).not.toContain("/provision");
    expect(
      new Headers(fetchMock.mock.calls[0][1]?.headers).get("Idempotency-Key"),
    ).toBe("deploy-attempt-1");
    expect(result.job_id).toBe("job-123");
  });

  it("restarts provisioning from the persisted Wizard stack", async () => {
    const result = await provisionStack("stack-123", "provision-attempt-1");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/stacks/stack-123/provision"),
      expect.objectContaining({ method: "POST" }),
    );
    expect(
      new Headers(fetchMock.mock.calls[0][1]?.headers).get("Idempotency-Key"),
    ).toBe("provision-attempt-1");
    expect(result.job_id).toBe("job-123");
  });

  it("resumes an enrollment wait with the exact source job and lease", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            success: true,
            message: "Enrollment rollout recovery accepted",
            kit_deployment_id: "stack-123",
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
      kit_deployment_id: "stack-123",
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
            kit_deployment_id: "stack-123",
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
    expect(result).toMatchObject({
      kit_deployment_id: "stack-123",
      provider_vm_create_requested: false,
    });
  });

  it("loads one StackKit deployment through the compatibility detail endpoint", async () => {
    const result = await getKitDeployment("stack-123");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/stacks/stack-123"),
      expect.objectContaining({ method: "GET" }),
    );
    expect(result.id).toBe("stack-123");
    expect(result.kit_deployment_id).toBe("stack-123");
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

  it("calls monthly runtime status and supported action endpoints by lease id", async () => {
    await getMonthlyRuntimeStatus("lease-123", "org-1");
    await decommissionMonthlyRuntime("lease-123", "org-1");
    await reconnectMonthlyRuntime("lease-123", "org-1");

    expect(String(fetchMock.mock.calls[0][0])).toContain(
      "/api/v1/monthly-runtimes/lease-123?tenant_id=org-1",
    );
    expect(fetchMock.mock.calls[1]).toEqual([
      expect.stringContaining(
        "/api/v1/monthly-runtimes/lease-123/decommission?tenant_id=org-1",
      ),
      expect.objectContaining({ method: "POST" }),
    ]);
    expect(fetchMock.mock.calls[2]).toEqual([
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
    expect(result.kit_deployment_id).toBe("stack-123");
  });

  it("unwraps validation and import envelopes for stack-spec import", async () => {
    const validation = await validateKombinationImport("name: imported");
    const result = await importKombinationSpec("name: imported");

    expect(validation.valid).toBe(true);
    expect(validation.warnings?.[0].code).toBe("preview");
    expect(result.kit_deployment_id).toBe("stack-imported");
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
            kit_deployment_id: "stack-raw",
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
    expect(result.kit_deployment_id).toBe("stack-raw");
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
    expect(result.server.kit_deployment_id).toBe("stack-123");
    expect(result.kit_deployment_id).toBe("stack-123");
  });

  it("starts a StackKit lifecycle operation for one kit deployment", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            job_id: "job-lifecycle-1",
            kit_deployment_id: "stack-123",
            agent_id: "agent-1",
            operation: "verify",
            status: "accepted",
          },
        }),
        { status: 202, headers: { "content-type": "application/json" } },
      ),
    );

    const result = await runStackKitLifecycleOperation("stack-123", {
      operation: "verify",
      agent_id: "agent-1",
    });

    expect(String(fetchMock.mock.calls[0][0])).toContain(
      "/api/v1/stacks/stack-123/stackkit/operations",
    );
    expect(result).toMatchObject({
      kit_deployment_id: "stack-123",
      job_id: "job-lifecycle-1",
      operation: "verify",
    });
  });

  it("reviews exact projection cleanup before mutation", async () => {
    const result = await planOrphanCleanup();

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/stacks/prune-orphans"),
      expect.objectContaining({ method: "POST", body: '{"mode":"dry_run"}' }),
    );
    expect(result.digest).toBe("sha256:cleanup-plan");
    expect(result.candidates[0]?.id).toBe("stack-123");
  });

  it("applies one targeted plan by its exact digest", async () => {
    await applyOrphanCleanup("sha256:cleanup-plan", "legacy stack/1");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/stacks/prune-orphans"),
      expect.objectContaining({
        method: "POST",
        body: '{"mode":"apply","digest":"sha256:cleanup-plan","stack_id":"legacy stack/1"}',
      }),
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
    expect(result.kit_deployment_id).toBe("stack-123");
    expect(result.lease_id).toBe("lease-stack-123-worker-abcd1234");
  });
});
