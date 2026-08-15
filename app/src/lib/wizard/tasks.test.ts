/**
 * Unit tests for wizard tasks module
 *
 * Tests the task list creation, status updates, requirements calculation,
 * and install command generation functions.
 */

import { describe, it, expect } from "vitest";
import {
  createTaskList,
  mapJobStatusToTaskStatus,
  updateTasksFromJob,
  updateTasksWithError,
  parseManagedRuntimeProviderError,
  buildManagedRuntimeProviderErrorDetails,
  calculateRequirements,
  generateInstallCommand,
  generateInstallCommands,
  DEFAULT_TASKS,
  RUNTIME_TASKS,
  createRuntimeTaskList,
  createAddServerTaskList,
  TASK_GROUPS,
  computeGroupStatus,
  computeGroupStatuses,
  isPostLeaseRuntimeTask,
  isStackKitArtifactOrRoutingTask,
} from "./tasks";

describe("createTaskList", () => {
  it("should create a list with all default tasks", () => {
    const tasks = createTaskList();
    expect(tasks).toHaveLength(DEFAULT_TASKS.length);
  });

  it("should initialize all tasks with pending status", () => {
    const tasks = createTaskList();
    tasks.forEach((task) => {
      expect(task.status).toBe("pending");
    });
  });

  it("should include all required task IDs", () => {
    const tasks = createTaskList();
    const expectedIds = DEFAULT_TASKS.map((t) => t.id);
    const taskIds = tasks.map((t) => t.id);
    expectedIds.forEach((id) => {
      expect(taskIds).toContain(id);
    });
  });
});

describe("createRuntimeTaskList", () => {
  it("tracks the full Cloud Kit preparation and runtime orchestration phases", () => {
    const tasks = createRuntimeTaskList();
    const ids = tasks.map((task) => task.id);

    expect(ids).toEqual([
      ...DEFAULT_TASKS.map((task) => task.id),
      ...RUNTIME_TASKS.map((task) => task.id),
    ]);
    expect(ids).toEqual([
      "validate",
      "save_config",
      "find_stackkit",
      "unify_services",
      "unify_network",
      "unify_security",
      "unify_auth",
      "create_spec",
      "create_lease",
      "prepare_rollout",
      "runtime_connected",
      "telemetry_handshake",
      "validate_workers",
      "generate_unified",
      "persist_unified",
      "generate_iac",
      "simulate_update",
      "stackkit_prepare",
      "docker_ready",
      "opentofu_ready",
      "terramate_ready",
      "telemetry_ready",
      "stackkit_rollout",
      "service_inventory",
      "verify_rollout",
      "restore_drill",
    ]);
    expect(tasks.every((task) => task.status === "pending")).toBe(true);
  });
});

describe("createAddServerTaskList", () => {
  it("uses a short managed-runtime request flow for Add Server", () => {
    const tasks = createAddServerTaskList("kombify-cloud");

    expect(tasks.map((task) => task.id)).toEqual(["create_lease"]);
    expect(tasks.every((task) => task.status === "pending")).toBe(true);

    const groups = computeGroupStatuses(tasks);
    expect(groups.map((group) => group.group.id)).toEqual(["provision"]);
    expect(groups[0].total).toBe(1);
  });

  it("uses a short registration flow for user-owned Add Server", () => {
    const tasks = createAddServerTaskList("install-command");

    expect(tasks.map((task) => task.id)).toEqual(["create_spec"]);
    expect(computeGroupStatuses(tasks).map((group) => group.group.id)).toEqual([
      "configure",
    ]);
  });
});

describe("mapJobStatusToTaskStatus", () => {
  it('should map "pending" to "pending"', () => {
    expect(mapJobStatusToTaskStatus("pending")).toBe("pending");
  });

  it('should map "queued" to "pending"', () => {
    expect(mapJobStatusToTaskStatus("queued")).toBe("pending");
  });

  it('should map "running" to "running"', () => {
    expect(mapJobStatusToTaskStatus("running")).toBe("running");
  });

  it('should map "in_progress" to "running"', () => {
    expect(mapJobStatusToTaskStatus("in_progress")).toBe("running");
  });

  it('should map resumable "waiting" to an active task', () => {
    expect(mapJobStatusToTaskStatus("waiting")).toBe("running");
  });

  it('should map "completed" to "completed"', () => {
    expect(mapJobStatusToTaskStatus("completed")).toBe("completed");
  });

  it('should map "success" to "completed"', () => {
    expect(mapJobStatusToTaskStatus("success")).toBe("completed");
  });

  it('should map "failed" to "failed"', () => {
    expect(mapJobStatusToTaskStatus("failed")).toBe("failed");
  });

  it('should map "error" to "failed"', () => {
    expect(mapJobStatusToTaskStatus("error")).toBe("failed");
  });

  it('should map unknown status to "pending"', () => {
    expect(mapJobStatusToTaskStatus("unknown")).toBe("pending");
    expect(mapJobStatusToTaskStatus("")).toBe("pending");
  });
});

describe("updateTasksFromJob", () => {
  it("should mark all tasks as completed when job is completed", () => {
    const tasks = createTaskList();
    const updated = updateTasksFromJob(tasks, { state: "completed" });
    updated.forEach((task) => {
      expect(task.status).toBe("completed");
    });
  });

  it("should mark all tasks as completed when job status is success", () => {
    const tasks = createTaskList();
    const updated = updateTasksFromJob(tasks, { status: "success" });
    updated.forEach((task) => {
      expect(task.status).toBe("completed");
    });
  });

  it("should not complete runtime tasks for a completed job without verified rollout proof", () => {
    const tasks = createRuntimeTaskList();
    const updated = updateTasksFromJob(tasks, {
      state: "completed",
      result: {
        runtime_phase: "deployed",
        verification_status: "deployed",
      },
    });

    expect(updated.find((task) => task.id === "create_spec")?.status).toBe(
      "completed",
    );
    expect(updated.find((task) => task.id === "stackkit_rollout")?.status).toBe(
      "pending",
    );
    expect(updated.find((task) => task.id === "verify_rollout")?.status).toBe(
      "pending",
    );
  });

  it("should complete runtime tasks only when verified rollout proof exists", () => {
    const tasks = createRuntimeTaskList();
    const updated = updateTasksFromJob(tasks, {
      state: "completed",
      result: {
        runtime_phase: "verified",
        verification_status: "verified",
        e2e_proof: { restore_result: "verified" },
      },
    });

    updated.forEach((task) => {
      expect(task.status).toBe("completed");
    });
  });

  it("completes the short Add Server task on add-server completion", () => {
    const tasks = createAddServerTaskList("kombify-cloud");
    const updated = updateTasksFromJob(tasks, {
      state: "completed",
      result: {
        creation_operation: "add-server",
        server_provisioning_mode: "kombify-cloud",
        runtime_phase: "lease_requested",
      },
    });

    expect(updated).toHaveLength(1);
    expect(updated[0]).toMatchObject({
      id: "create_lease",
      status: "completed",
    });
  });

  it("treats managed runtime additions as Add Server completion even without the operation field", () => {
    const tasks = createAddServerTaskList("kombify-cloud");
    const updated = updateTasksFromJob(tasks, {
      state: "completed",
      result: {
        managed_runtime_addition: true,
        server_provisioning_mode: "kombify-cloud",
      },
    });

    expect(updated[0].status).toBe("completed");
  });

  it("shrinks running Add Server jobs to the short managed server task list", () => {
    const tasks = createRuntimeTaskList();
    const updated = updateTasksFromJob(tasks, {
      state: "running",
      step: "create_lease",
      message: "Managed runtime server requested",
      result: {
        creation_operation: "add-server",
        server_provisioning_mode: "kombify-cloud",
      },
    });

    expect(updated).toHaveLength(1);
    expect(updated[0]).toMatchObject({
      id: "create_lease",
      status: "running",
      message: "Managed runtime server requested",
    });
  });

  it("should mark first task as failed when job fails with no running task", () => {
    const tasks = createTaskList();
    const updated = updateTasksFromJob(tasks, { state: "failed" });
    expect(updated[0].status).toBe("failed");
    updated.slice(1).forEach((task) => {
      expect(task.status).toBe("pending");
    });
  });

  it("should mark running task as failed when job fails", () => {
    const tasks = createTaskList();
    tasks[4].status = "running"; // unify_network task is running
    tasks[0].status = "completed";
    tasks[1].status = "completed";
    tasks[2].status = "completed";
    tasks[3].status = "completed";

    const updated = updateTasksFromJob(tasks, { state: "failed" });

    expect(updated[0].status).toBe("completed");
    expect(updated[1].status).toBe("completed");
    expect(updated[4].status).toBe("failed");
    expect(updated[5].status).toBe("pending");
  });

  it("should not fabricate completed tasks from progress percentage without a backend step", () => {
    const tasks = createTaskList();
    const updated = updateTasksFromJob(tasks, {
      state: "running",
      progress: 50,
    });

    const completedCount = updated.filter(
      (t) => t.status === "completed",
    ).length;
    expect(completedCount).toBe(0);

    const runningTask = updated.find((t) => t.status === "running");
    expect(runningTask).toBeFalsy();
  });

  it("keeps a resumable waiting message on the active task without fabricating completion", () => {
    const tasks = createRuntimeTaskList();
    const updated = updateTasksFromJob(tasks, {
      state: "waiting",
      progress: 20,
      message: "Enrollment checkpoint scheduled",
    });

    expect(updated.filter((task) => task.status === "completed")).toHaveLength(
      0,
    );
    expect(updated.filter((task) => task.status === "failed")).toHaveLength(0);
    expect(updated.find((task) => task.status === "running")).toMatchObject({
      message: "Enrollment checkpoint scheduled",
      progress: 20,
    });
  });

  it("should mark specific step as running when step ID provided", () => {
    const tasks = createTaskList();
    const updated = updateTasksFromJob(tasks, {
      state: "running",
      step: "unify_services",
      progress: 40,
    });

    // unify_services task should be running
    const servicesTask = updated.find((t) => t.id === "unify_services");
    expect(servicesTask?.status).toBe("running");
  });

  it("should use backend step order before progress when runtime rollout advances", () => {
    const tasks = createRuntimeTaskList();
    const updated = updateTasksFromJob(tasks, {
      state: "running",
      step: "stackkit_rollout",
      progress: 75,
    });

    expect(updated.find((t) => t.id === "generate_iac")?.status).toBe(
      "completed",
    );
    expect(updated.find((t) => t.id === "stackkit_rollout")?.status).toBe(
      "running",
    );
    expect(updated.find((t) => t.id === "verify_rollout")?.status).toBe(
      "pending",
    );
  });

  it("should attach the backend status message to the running task", () => {
    const tasks = createRuntimeTaskList();
    const updated = updateTasksFromJob(tasks, {
      state: "running",
      step: "prepare_rollout",
      current_step: "Managed VM lease is still enrolling (45s of 14m30s)...",
    });

    const running = updated.find((task) => task.id === "prepare_rollout");
    expect(running?.status).toBe("running");
    expect(running?.message).toContain("still enrolling");
  });

  it("should keep a visible running task when backend has only human status text", () => {
    const tasks = createRuntimeTaskList();
    tasks[0].status = "completed";
    tasks[1].status = "completed";

    const updated = updateTasksFromJob(tasks, {
      state: "running",
      current_step: "Managed VM lease is still enrolling...",
      progress: 10,
    });

    const running = updated.find((task) => task.id === "prepare_rollout");
    expect(running?.status).toBe("running");
    expect(running?.message).toBe("Managed VM lease is still enrolling...");
    expect(updated.find((task) => task.id === "verify_rollout")?.status).toBe(
      "pending",
    );
  });

  it("maps StackKits prepare progress text to granular rollout substeps", () => {
    const tasks = createRuntimeTaskList();
    const updated = updateTasksFromJob(tasks, {
      state: "running",
      step: "stackkit_rollout",
      message: "phase=apt_wait status=begin",
      progress: 74,
    });

    expect(updated.find((task) => task.id === "docker_ready")?.status).toBe(
      "running",
    );
    expect(updated.find((task) => task.id === "stackkit_prepare")?.status).toBe(
      "completed",
    );
    expect(updated.find((task) => task.id === "stackkit_rollout")?.status).toBe(
      "pending",
    );
  });

  it("should not expose a legacy machine current_step id as status copy", () => {
    const tasks = createRuntimeTaskList();
    const updated = updateTasksFromJob(tasks, {
      state: "running",
      current_step: "prepare_rollout",
    });

    const running = updated.find((task) => task.id === "prepare_rollout");
    expect(running?.status).toBe("running");
    expect(running?.message).not.toBe("prepare_rollout");
  });

  it("should prefer the explicit failed backend step over a stale running runtime task", () => {
    const tasks = createRuntimeTaskList();
    const prepare = tasks.find((task) => task.id === "prepare_rollout");
    if (!prepare) throw new Error("missing prepare_rollout task");
    prepare.status = "running";

    const updated = updateTasksFromJob(tasks, {
      state: "failed",
      step: "generate_iac",
    });

    expect(updated.find((task) => task.id === "prepare_rollout")?.status).toBe(
      "completed",
    );
    expect(updated.find((task) => task.id === "validate_workers")?.status).toBe(
      "completed",
    );
    expect(updated.find((task) => task.id === "generate_iac")?.status).toBe(
      "failed",
    );
    expect(updated.find((task) => task.id === "stackkit_rollout")?.status).toBe(
      "pending",
    );
  });
});

describe("updateTasksWithError", () => {
  it("should mark first task as failed with error message", () => {
    const tasks = createTaskList();
    const updated = updateTasksWithError(tasks, {
      error: "Validation failed",
      error_details: "Invalid YAML syntax at line 42",
    });

    expect(updated[0].status).toBe("failed");
    // updateTasksWithError maps raw errors to a user-friendly localized message
    expect(updated[0].errorMessage).toBe(
      "Configuration could not be validated",
    );
    expect(updated[0].errorDetails).toBe("Invalid YAML syntax at line 42");
  });

  it("should mark running task as failed when specified", () => {
    const tasks = createTaskList();
    tasks[0].status = "completed";
    tasks[1].status = "completed";
    tasks[4].status = "running";

    const updated = updateTasksWithError(tasks, {
      error: "Network configuration failed",
      error_details: "Could not reach DNS server",
    });

    expect(updated[0].status).toBe("completed");
    expect(updated[1].status).toBe("completed");
    expect(updated[4].status).toBe("failed");
    expect(updated[4].errorMessage).toBe("Network error while saving");
    expect(updated[5].status).toBe("pending");
  });

  it("should mark specific step as failed when step ID provided", () => {
    const tasks = createTaskList();
    const updated = updateTasksWithError(tasks, {
      step: "unify_security",
      error: "Certificate generation failed",
    });

    const securityTask = updated.find((t) => t.id === "unify_security");
    expect(securityTask?.status).toBe("failed");
    // Unknown errors fall back to generic troubleshooting
    expect(securityTask?.errorMessage).toBe("Unexpected error");
  });

  it("should use default error message when none provided", () => {
    const tasks = createTaskList();
    const updated = updateTasksWithError(tasks, {});

    expect(updated[0].status).toBe("failed");
    expect(updated[0].errorMessage).toBe("Unexpected error");
  });

  it("should expose managed runtime lease failures with the raw backend cause", () => {
    const tasks = createRuntimeTaskList();
    const updated = updateTasksWithError(tasks, {
      step: "prepare_rollout",
      error:
        'managed runtime lease target was not available before timeout 14m30s: managed runtime lease address missing for lease "lease-1"; wait until VM lease enrollment exposes runtime_ssh_host or runtime_public_ip, then retry rollout',
      error_details:
        "Managed Runtime rollout requires an SSH host or public IP from the VM lease before StackKits can generate and apply the rollout.",
    });

    const failed = updated.find((task) => task.id === "prepare_rollout");
    expect(failed?.status).toBe("failed");
    expect(failed?.errorMessage).toBe("Managed Runtime is not ready yet");
    expect(failed?.errorDetails).toContain("runtime_ssh_host");
    expect(failed?.errorDetails).toContain("runtime_public_ip");
  });

  it("should expose provider enrollment failures instead of a generic rollout detail", () => {
    const tasks = createRuntimeTaskList();
    const updated = updateTasksWithError(tasks, {
      step: "prepare_rollout",
      error:
        'managed runtime lease address is not available yet for lease "lease-1": managed runtime lease enrollment failed for lease "lease-1": simulate enroll returned 502: {"error":{"code":502,"message":"create ionos-managed node: ionos create server request failed: [VDC-5-1091] Storage creation on SSD failed due to too many recent create and delete operations. Please contact support."}}',
      error_details:
        "Managed Runtime rollout requires an SSH host or public IP from the VM lease before StackKits can generate and apply the rollout.",
    });

    const failed = updated.find((task) => task.id === "prepare_rollout");
    expect(failed?.status).toBe("failed");
    expect(failed?.errorMessage).toBe("Managed Runtime could not be created");
    expect(failed?.errorDetails).toContain("[VDC-5-1091]");
    expect(failed?.errorDetails).toContain(
      "too many recent create and delete operations",
    );
    expect(failed?.errorDetails).toContain("Provider: IONOS");
    expect(failed?.errorDetails).toContain("Error code: VDC-5-1091");
    expect(failed?.errorDetails).toContain("cooldown");
  });

  it("should distinguish managed runtime target bootstrap failures from lease readiness", () => {
    const tasks = createRuntimeTaskList();
    const updated = updateTasksWithError(tasks, {
      step: "stackkit_rollout",
      error:
        "Managed runtime target bootstrap failed: bootstrap managed runtime target: wait: remote command exited without exit status or exit signal",
      error_details:
        "TechStack could not prepare the managed VPS for the StackKits rollout.",
    });

    const failed = updated.find((task) => task.id === "stackkit_rollout");
    expect(failed?.status).toBe("failed");
    expect(failed?.errorMessage).toBe("Managed Runtime could not be prepared");
    expect(failed?.errorDetails).toContain("managed VPS");
    expect(failed?.errorDetails).toContain("without exit status");
    expect(failed?.troubleshooting).toContain(
      "Check cloud-init, Docker status, and SSH reachability on the Managed Runtime server",
    );
  });

  it("should classify StackKits rollout validation failures as rollout blockers", () => {
    const tasks = createRuntimeTaskList();
    const updated = updateTasksWithError(tasks, {
      step: "stackkit_rollout",
      error:
        "StackKits rollout failed: runtime action stackkit_rollout returned 422: configuration could not be validated",
      error_details: "StackKits could not apply the selected StackKit rollout.",
    });

    const failed = updated.find((task) => task.id === "stackkit_rollout");
    expect(failed?.status).toBe("failed");
    expect(failed?.errorMessage).toBe("StackKit rollout could not be applied");
    expect(failed?.errorMessage).not.toBe(
      "Configuration could not be validated",
    );
    expect(failed?.errorDetails).toContain(
      "StackKits could not apply the selected StackKit rollout.",
    );
    expect(failed?.errorDetails).toContain("stackkit_rollout returned 422");
    expect(failed?.troubleshooting).toContain(
      "Check the error details for backend error, target bootstrap, and runtime diagnostics",
    );
  });

  it("should classify kombify.me artifact quota failures separately from StackKit matching", () => {
    const tasks = createRuntimeTaskList();
    const prepare = tasks.find((task) => task.id === "prepare_rollout");
    if (!prepare) throw new Error("missing prepare_rollout task");
    prepare.status = "running";

    const incidentError =
      'StackKits artifact generation failed: StackKits CLI generate failed: exit status 1: Error: kombify.me registration failed and no subdomainPrefix is configured: auto-register base subdomain: API error 429: {"error":"base subdomain limit reached (max 5 per user)"}';
    const updated = updateTasksWithError(tasks, {
      step: "generate_iac",
      error: incidentError,
      error_details:
        "Could not generate StackKits rollout artifacts.\n\nBackend error:\n" +
        incidentError,
    });

    const failed = updated.find((task) => task.id === "generate_iac");
    expect(updated.find((task) => task.id === "prepare_rollout")?.status).toBe(
      "completed",
    );
    expect(failed?.status).toBe("failed");
    expect(failed?.errorMessage).toBe(
      "StackKit artifacts could not be generated",
    );
    expect(failed?.errorMessage).not.toBe("No matching StackKit found");
    expect(failed?.errorDetails).toContain("base subdomain limit reached");
    expect(failed?.errorDetails).toContain("API error 429");
    expect(failed?.troubleshooting).toContain(
      "Do not create another provider VM; reuse the existing stack and lease for the next rollout attempt",
    );
  });

  it("should parse managed runtime provider error codes for feedback and logging", () => {
    const info = parseManagedRuntimeProviderError(
      'managed runtime lease enrollment failed for lease "lease-1": simulate enroll returned 502: {"error":{"code":502,"message":"create ionos-managed node: ionos create server request failed: [VDC-5-1091] Storage creation on SSD failed due to too many recent create and delete operations. Please contact support."}}',
    );

    expect(info.isProviderError).toBe(true);
    expect(info.provider).toBe("ionos-managed");
    expect(info.providerLabel).toBe("IONOS");
    expect(info.code).toBe("VDC-5-1091");
    expect(info.category).toBe("provider_throttle");
    expect(info.retryHint).toBe("retry_after_provider_cooldown");
    expect(info.summary).toContain("Storage creation on SSD failed");
  });

  it("should build user-facing managed runtime provider details", () => {
    const details = buildManagedRuntimeProviderErrorDetails(
      'managed runtime lease enrollment failed for lease "lease-1": simulate enroll returned 502: {"error":{"code":502,"message":"create ionos-managed node: ionos create server request failed: [VDC-5-1091] Storage creation on SSD failed due to too many recent create and delete operations. Please contact support."}}',
      "Managed Runtime rollout requires an SSH host or public IP from the VM lease before StackKits can generate and apply the rollout.",
    );

    expect(details).toContain("The cloud provider");
    expect(details).toContain("Provider: IONOS");
    expect(details).toContain("Error code: VDC-5-1091");
    expect(details).toContain("Provider message:");
    expect(details).toContain("cooldown");
    expect(details).toContain("Technical details:");
  });

  it("should not set errorDetails when not provided", () => {
    const tasks = createTaskList();
    const updated = updateTasksWithError(tasks, {
      error: "Something went wrong",
    });

    // updateTasksWithError always provides either backend error_details or a
    // helpful fallback troubleshooting detail string.
    expect(updated[0].errorDetails).toBeTruthy();
  });
});

describe("calculateRequirements", () => {
  it("should require 1 local server for local provider", () => {
    const result = calculateRequirements({ provider: "local" });

    expect(result.minLocalServers).toBe(1);
    expect(result.minCloudServers).toBe(0);
    expect(result.minTotalServers).toBe(1);
    expect(result.description).toContain("local server");
  });

  it("should require 1 cloud server for cloud provider", () => {
    const result = calculateRequirements({ provider: "cloud" });

    expect(result.minCloudServers).toBe(1);
    expect(result.minTotalServers).toBeGreaterThanOrEqual(1);
    expect(result.description).toContain("cloud server");
  });

  it("should require both cloud and local for hybrid provider", () => {
    const result = calculateRequirements({ provider: "hybrid" });

    expect(result.minCloudServers).toBe(1);
    expect(result.minLocalServers).toBe(1);
    expect(result.minTotalServers).toBe(2);
    expect(result.description).toContain("cloud server");
    expect(result.description).toContain("local server");
  });

  it("should require cloud server for anywhere access mode", () => {
    const result = calculateRequirements({
      provider: "local",
      network: { accessMode: "anywhere" },
    });

    expect(result.minCloudServers).toBe(1);
  });

  it("should add everything recommendation when everything goal is set", () => {
    const result = calculateRequirements({
      provider: "local",
      goals: { everything: true },
    });

    expect(result.details).toContain(
      "Complete setup: At least 1 capable server",
    );
  });

  it("should return default local requirements when no config provided", () => {
    const result = calculateRequirements({});

    expect(result.minLocalServers).toBe(1);
    expect(result.minTotalServers).toBe(1);
  });

  it("should not require a local server for managed kombify Cloud provisioning", () => {
    const result = calculateRequirements({
      provider: "cloud",
      network: { accessMode: "anywhere" },
      serverProvisioning: { mode: "kombify-cloud" },
    });

    expect(result.minCloudServers).toBe(1);
    expect(result.minLocalServers).toBe(0);
    expect(result.minTotalServers).toBe(1);
    expect(result.details).toContain(
      "A kombify Cloud server is provisioned automatically through the subscription",
    );
  });

  it("should describe direct remote server provisioning as an existing server", () => {
    const result = calculateRequirements({
      provider: "local",
      network: { accessMode: "anywhere" },
      serverProvisioning: { mode: "connect-remote" },
    });

    expect(result.minCloudServers).toBe(0);
    expect(result.minLocalServers).toBe(1);
    expect(result.description).toContain("existing server");
    expect(result.details).toContain(
      "An existing remote server is connected over SSH",
    );
  });

  it("should describe one-liner provisioning as user-owned server registration", () => {
    const result = calculateRequirements({
      provider: "local",
      serverProvisioning: { mode: "install-command" },
    });

    expect(result.minCloudServers).toBe(0);
    expect(result.minLocalServers).toBe(1);
    expect(result.description).toContain("user-owned server");
    expect(result.details).toContain(
      "The installation command runs on your own server or device",
    );
  });
});

describe("generateInstallCommand", () => {
  it("should generate correct curl command", () => {
    const command = generateInstallCommand(
      "https://techstack.local",
      "test_token_123",
    );

    expect(command).toContain("curl -fsSL");
    expect(command).toContain("https://techstack.local/install.sh");
    expect(command).toContain('KOMBI_SERVER="https://techstack.local"');
    expect(command).toContain('KOMBI_TOKEN="test_token_123"');
    expect(command).toContain("TECHSTACK_AS_SERVICE=1");
    expect(command).toContain("bash");
  });

  it("should remove trailing slash from server URL", () => {
    const command = generateInstallCommand("https://techstack.local/", "token");

    expect(command).not.toContain("local//install.sh");
    expect(command).toContain("local/install.sh");
  });

  it("should handle http URLs", () => {
    const command = generateInstallCommand(
      "http://techstack.local:5260",
      "my_token",
    );

    expect(command).toContain("http://techstack.local:5260/install.sh");
    expect(command).toContain('KOMBI_SERVER="http://techstack.local:5260"');
  });

  it("should normalize dev UI port 5261 to core API port 5260", () => {
    const command = generateInstallCommand("http://localhost:5261", "token");

    expect(command).toContain("http://localhost:5260/install.sh");
    expect(command).toContain('KOMBI_SERVER="http://localhost:5260"');
  });
});

describe("TASK_GROUPS", () => {
  it("covers every default + runtime task id exactly once", () => {
    const allTaskIds = [...DEFAULT_TASKS, ...RUNTIME_TASKS].map((t) => t.id);
    const groupedIds = TASK_GROUPS.flatMap((g) => g.taskIds);

    expect(groupedIds.sort()).toEqual(allTaskIds.sort());

    const seen = new Set<string>();
    for (const id of groupedIds) {
      expect(seen.has(id)).toBe(false);
      seen.add(id);
    }
  });

  it("orders groups so unifier work comes before runtime work", () => {
    const ids = TASK_GROUPS.map((g) => g.id);
    expect(ids[0]).toBe("configure");
    expect(ids[ids.length - 1]).toBe("verify");
  });

  it("keeps managed VPS readiness separate from StackKits artifact and rollout phases", () => {
    expect(isPostLeaseRuntimeTask("create_lease")).toBe(false);
    expect(isPostLeaseRuntimeTask("prepare_rollout")).toBe(true);
    expect(isPostLeaseRuntimeTask("docker_ready")).toBe(true);
    expect(isPostLeaseRuntimeTask("generate_iac")).toBe(true);

    expect(isStackKitArtifactOrRoutingTask("prepare_rollout")).toBe(false);
    expect(isStackKitArtifactOrRoutingTask("validate_workers")).toBe(false);
    expect(isStackKitArtifactOrRoutingTask("docker_ready")).toBe(true);
    expect(isStackKitArtifactOrRoutingTask("generate_iac")).toBe(true);
    expect(isStackKitArtifactOrRoutingTask("stackkit_rollout")).toBe(true);
  });
});

describe("computeGroupStatus", () => {
  it("returns pending when all sub-tasks are pending", () => {
    const tasks = createRuntimeTaskList();
    const configure = TASK_GROUPS.find((g) => g.id === "configure")!;

    const status = computeGroupStatus(tasks, configure);

    expect(status.status).toBe("pending");
    expect(status.completed).toBe(0);
    expect(status.total).toBe(8);
    expect(status.tasks.map((task) => task.id)).toEqual([
      "validate",
      "find_stackkit",
      "create_spec",
    ]);
    expect(status.runningTask).toBeUndefined();
    expect(status.failedTask).toBeUndefined();
  });

  it("returns running when any sub-task is running and reports the running task", () => {
    const tasks = createRuntimeTaskList();
    const updated = updateTasksFromJob(tasks, {
      state: "running",
      step: "unify_network",
    });
    const configure = TASK_GROUPS.find((g) => g.id === "configure")!;

    const status = computeGroupStatus(updated, configure);

    expect(status.status).toBe("running");
    expect(status.runningTask?.id).toBe("unify_network");
    expect(status.completed).toBe(4);
    expect(status.total).toBe(8);
  });

  it("returns completed when every sub-task is completed", () => {
    const tasks = createRuntimeTaskList();
    const updated = updateTasksFromJob(tasks, {
      state: "running",
      step: "create_lease",
    });
    const configure = TASK_GROUPS.find((g) => g.id === "configure")!;

    const status = computeGroupStatus(updated, configure);

    expect(status.status).toBe("completed");
    expect(status.completed).toBe(8);
  });

  it("returns failed when any sub-task is failed and surfaces the failed task", () => {
    const tasks = createRuntimeTaskList();
    const updated = updateTasksWithError(tasks, {
      step: "create_lease",
      error: "lease provisioning failed",
    });
    const provision = TASK_GROUPS.find((g) => g.id === "provision")!;

    const status = computeGroupStatus(updated, provision);

    expect(status.status).toBe("failed");
    expect(status.failedTask?.id).toBe("create_lease");
  });

  it("returns total 0 when no sub-tasks are present (non-cloud unifier flow)", () => {
    const tasks = createTaskList();
    const rollout = TASK_GROUPS.find((g) => g.id === "rollout")!;

    const status = computeGroupStatus(tasks, rollout);

    expect(status.total).toBe(0);
    expect(status.tasks).toEqual([]);
    expect(status.status).toBe("pending");
  });
});

describe("computeGroupStatuses", () => {
  it("drops groups with no matching sub-tasks on the unifier-only flow", () => {
    const tasks = createTaskList();
    const statuses = computeGroupStatuses(tasks);

    expect(statuses).toHaveLength(1);
    expect(statuses[0].group.id).toBe("configure");
    expect(statuses[0].total).toBe(8);
  });

  it("returns all five phases on the kombify-cloud runtime flow", () => {
    const tasks = createRuntimeTaskList();
    const statuses = computeGroupStatuses(tasks);

    expect(statuses.map((s) => s.group.id)).toEqual([
      "configure",
      "provision",
      "generate",
      "rollout",
      "verify",
    ]);
    expect(statuses.reduce((sum, s) => sum + s.total, 0)).toBe(
      DEFAULT_TASKS.length + RUNTIME_TASKS.length,
    );
  });
});

describe("generateInstallCommands", () => {
  it("should return commands for linux, docker, and manual platforms", () => {
    const commands = generateInstallCommands("https://example.com", "token123");

    expect(commands).toHaveLength(3);

    const platforms = commands.map((c) => c.platform);
    expect(platforms).toContain("linux");
    expect(platforms).toContain("docker");
    expect(platforms).toContain("manual");
  });

  it("should generate valid linux curl command", () => {
    const commands = generateInstallCommands("https://example.com", "token");
    const linuxCmd = commands.find((c) => c.platform === "linux");

    expect(linuxCmd?.command).toContain("curl");
    expect(linuxCmd?.command).toContain("bash");
    expect(linuxCmd?.command).toContain("TECHSTACK_AS_SERVICE=1");
    expect(linuxCmd?.description).toContain("Linux");
    expect(linuxCmd?.description).toContain("persistent outbound Guard");
  });

  it("should generate valid docker run command", () => {
    const commands = generateInstallCommands("https://example.com", "mytoken");
    const dockerCmd = commands.find((c) => c.platform === "docker");

    expect(dockerCmd?.command).toContain("docker run -d");
    expect(dockerCmd?.command).toContain("--name techstack-agent");
    expect(dockerCmd?.command).toContain("-e KOMBI_SERVER=");
    expect(dockerCmd?.command).toContain("-e KOMBI_TOKEN=");
    expect(dockerCmd?.command).toContain("techstack/agent:latest");
  });

  it("should generate valid manual command", () => {
    const commands = generateInstallCommands("https://example.com", "token");
    const manualCmd = commands.find((c) => c.platform === "manual");

    expect(manualCmd?.command).toContain("techstack agent register");
    expect(manualCmd?.command).toContain("--server");
    expect(manualCmd?.command).toContain("--token");
  });

  it("should include descriptions for all commands", () => {
    const commands = generateInstallCommands("https://example.com", "token");

    commands.forEach((cmd) => {
      expect(cmd.description).toBeTruthy();
      expect(cmd.description.length).toBeGreaterThan(0);
    });
  });
});
