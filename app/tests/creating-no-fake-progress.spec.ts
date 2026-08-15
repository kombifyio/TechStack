import { test, expect, type BrowserContext } from "@playwright/test";
import { mockLoggedInContext, requireAppBase } from "./helpers/test-utils";

/**
 * Creation flow honesty regressions
 *
 * These tests pin down the post-rollout/creating page invariants introduced
 * when fake "verified" completion was removed:
 *
 *  - The task list MUST NOT fabricate "completed" status for tasks the
 *    backend has not actually emitted a step transition for.
 *  - For kombify-cloud, the success card MUST NOT appear when the backend
 *    response is missing the StackKit identity handoff (stackkit_outputs).
 *  - When stackkit_outputs is complete, the success card and managed-runtime
 *    lease block must render with real values from job.result.
 */

const COMPLETE_HANDOFF = {
  identity: {
    owner: {
      username: "owner@example.com",
      email: "owner@example.com",
      displayName: "Owner Admin",
    },
    recovery: {
      bundle_ref: "wallet://stack_test/recovery",
      passphrase_hash_present: true,
    },
  },
  login_gateway: {
    url: "https://techstack.kombify.io/login",
    label: "Open first login",
  },
};
const VERIFIED_ROLLOUT_TITLE = "Your Homelab is ready";

async function mockDiscovery(context: BrowserContext) {
  await context.route("**/api/v1/discovery/networks", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ networks: [] }),
    });
  });
}

async function mockJob(
  context: BrowserContext,
  jobId: string,
  body: Record<string, unknown>,
) {
  // The frontend polls /api/v1/jobs/{id}. Keep a parallel mock for the legacy
  // PocketBase collections route so a frontend regression to the older endpoint
  // does not silently pass.
  const payload = JSON.stringify(body);
  await context.route(`**/api/v1/jobs/${jobId}`, async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: payload,
    });
  });
  await context.route(
    `**/api/collections/jobs/records/${jobId}**`,
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: payload,
      });
    },
  );
}

async function seedKombifyCloudConfig(context: BrowserContext, origin: string) {
  const page = await context.newPage();
  await page.goto(`${origin}/stacks/creating`);
  await page.evaluate(() => {
    sessionStorage.setItem(
      "creatingStackConfig",
      JSON.stringify({
        serverProvisioning: { mode: "kombify-cloud" },
      }),
    );
    sessionStorage.setItem("creatingStackName", "Test Stack");
  });
  await page.close();
}

test.describe("creation flow honesty", () => {
  test("does not show verified success when stackkit_outputs is missing", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await context.grantPermissions(["clipboard-read", "clipboard-write"], {
      origin,
    });
    await mockLoggedInContext(context, { allowMockAuth: true });
    await mockDiscovery(context);
    await mockJob(context, "job-no-handoff", {
      id: "job-no-handoff",
      type: "deploy",
      state: "completed",
      progress: 100,
      created: new Date().toISOString(),
      updated: new Date().toISOString(),
      result: {
        registration_token: "tok",
        stack_id: "stack-no-handoff",
        server_provisioning_mode: "kombify-cloud",
        runtime_phase: "deployed",
        verification_status: "deployed",
      },
    });

    await seedKombifyCloudConfig(context, origin);
    const page = await context.newPage();
    await page.goto(
      `${origin}/stacks/creating?name=Test%20Stack&job_id=job-no-handoff&stack_id=stack-no-handoff`,
    );

    // The success headline must NOT appear because StackKit did not hand back
    // the identity outputs that make the stack usable.
    await expect(
      page.getByRole("heading", {
        name: VERIFIED_ROLLOUT_TITLE,
        level: 2,
      }),
    ).toHaveCount(0, { timeout: 15_000 });
    // A clear failure / blocker indication must appear instead.
    await expect(
      page.getByRole("heading", { level: 2, name: "Rollout incomplete" }),
    ).toBeVisible({ timeout: 15_000 });

    await context.close();
  });

  test("does not auto-complete tasks the backend has not emitted", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await context.grantPermissions(["clipboard-read", "clipboard-write"], {
      origin,
    });
    await mockLoggedInContext(context, { allowMockAuth: true });
    await mockDiscovery(context);
    // Backend reports running with step=unify_security and nothing past it.
    await mockJob(context, "job-stuck", {
      id: "job-stuck",
      type: "provision",
      state: "running",
      progress: 40,
      step: "unify_security",
      created: new Date().toISOString(),
      updated: new Date().toISOString(),
    });

    await seedKombifyCloudConfig(context, origin);
    const page = await context.newPage();
    await page.goto(
      `${origin}/stacks/creating?name=Test%20Stack&job_id=job-stuck`,
    );

    // Wait for the polling loop to apply the backend response.
    await page.waitForTimeout(4500);

    // No fake completion sequence: tasks past unify_security must remain
    // pending. Pick a runtime task that the backend never emitted - if the
    // old startCompletionSequence is back, this assertion will fail.
    const verifyTaskCompleted = page.locator(
      '[data-task-id="verify_rollout"][data-status="completed"]',
    );
    await expect(verifyTaskCompleted).toHaveCount(0);
    const restoreTaskCompleted = page.locator(
      '[data-task-id="restore_drill"][data-status="completed"]',
    );
    await expect(restoreTaskCompleted).toHaveCount(0);

    await context.close();
  });

  test("keeps a visible running step when backend only emits human status text", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await context.grantPermissions(["clipboard-read", "clipboard-write"], {
      origin,
    });
    await mockLoggedInContext(context, { allowMockAuth: true });
    await mockDiscovery(context);
    await mockJob(context, "job-human-status", {
      id: "job-human-status",
      type: "provision",
      state: "running",
      progress: 10,
      current_step: "Managed VM lease is still enrolling...",
      created: new Date().toISOString(),
      updated: new Date().toISOString(),
    });

    await seedKombifyCloudConfig(context, origin);
    const page = await context.newPage();
    await page.goto(
      `${origin}/stacks/creating?name=Test%20Stack&job_id=job-human-status`,
    );

    await expect(page.getByText("Current status")).toBeVisible({
      timeout: 15_000,
    });
    await expect(
      page.getByText("Managed VM lease is still enrolling...").first(),
    ).toBeVisible();
    await expect(page.locator('[data-status="running"]').first()).toBeVisible();

    await context.close();
  });

  test("keeps enrollment waits non-terminal and access disabled beyond the pending timeout", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await mockLoggedInContext(context, { allowMockAuth: true });
    await mockDiscovery(context);
    await mockJob(context, "job-waiting-enrollment", {
      id: "job-waiting-enrollment",
      type: "provision",
      state: "waiting",
      progress: 20,
      step: "prepare_rollout",
      wait_reason: "waiting_enrollment",
      next_resume_at: "2026-07-19T08:15:00Z",
      resume_available_at: "2026-07-19T08:17:00Z",
      resume_available: false,
      message: "IONOS bereitet den Server noch vor.",
      created_at: "2026-07-19T08:00:00Z",
      updated_at: "2026-07-19T08:05:00Z",
      result: {
        stack_id: "stack-waiting-enrollment",
        server_provisioning_mode: "kombify-cloud",
        runtime_phase: "lease_requested",
      },
    });

    const page = await context.newPage();
    await page.clock.install({ time: new Date("2026-07-19T08:05:00Z") });
    await page.goto(
      `${origin}/stacks/creating?name=Waiting%20Stack&job_id=job-waiting-enrollment&stack_id=stack-waiting-enrollment`,
    );

    const waitingCard = page.getByTestId("waiting-enrollment-card");
    await expect(waitingCard).toBeVisible({ timeout: 15_000 });
    await expect(
      waitingCard.getByRole("heading", {
        level: 2,
        name: "Server wird noch bereitgestellt",
      }),
    ).toBeVisible();
    await expect(waitingCard).toContainText("Nächste geplante Prüfung");
    await expect(waitingCard).toContainText("keine neue VM");
    await expect(waitingCard).toContainText(
      "IONOS bereitet den Server noch vor.",
    );
    await expect(page.getByTestId("waiting-dashboard-disabled")).toBeDisabled();
    await expect(page.getByTestId("waiting-services-disabled")).toBeDisabled();
    await expect(page.getByTestId("waiting-access-disabled")).toBeDisabled();
    await expect(waitingCard.locator("a")).toHaveCount(0);

    // The generic queue watchdog must not reinterpret a backend-owned,
    // resumable enrollment wait as a crash after two minutes.
    await page.clock.fastForward(121_000);
    await expect(waitingCard).toBeVisible();
    await expect(
      page.getByRole("heading", { level: 2, name: "Creation failed" }),
    ).toHaveCount(0);
    await expect(
      page.getByText("Job is not being processed", { exact: true }),
    ).toHaveCount(0);

    await context.close();
  });

  test("recovers an overdue enrollment wait on the same lease without creating a VM", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await mockLoggedInContext(context, { allowMockAuth: true });
    await mockDiscovery(context);
    await mockJob(context, "job-overdue-enrollment", {
      id: "job-overdue-enrollment",
      type: "deploy",
      state: "waiting",
      progress: 20,
      step: "prepare_rollout",
      wait_reason: "waiting_enrollment",
      next_resume_at: "2026-07-19T08:00:00Z",
      resume_available_at: "2026-07-19T08:02:00Z",
      resume_available: true,
      message: "The scheduled enrollment check is overdue.",
      created_at: "2026-07-19T07:45:00Z",
      updated_at: "2026-07-19T08:00:00Z",
      result: {
        stack_id: "stack-overdue-enrollment",
        server_provisioning_mode: "kombify-cloud",
        runtime_phase: "lease_ready",
        lease_id: "lease-overdue-enrollment",
        lease_provider: "ionos-managed",
      },
    });
    await mockJob(context, "job-overdue-retry", {
      id: "job-overdue-retry",
      type: "deploy",
      state: "running",
      progress: 25,
      step: "prepare_rollout",
      message: "Continuing rollout on the existing VM.",
      created_at: "2026-07-19T08:05:00Z",
      updated_at: "2026-07-19T08:05:00Z",
      result: {
        stack_id: "stack-overdue-enrollment",
        lease_id: "lease-overdue-enrollment",
      },
    });

    let runtimeStatusCalls = 0;
    await context.route(
      "**/api/v1/monthly-runtimes/lease-overdue-enrollment",
      async (route) => {
        runtimeStatusCalls += 1;
        await route.abort();
      },
    );

    let resumeCalls = 0;
    let deployCalls = 0;
    let vmCreateCalls = 0;
    await context.route(
      "**/api/v1/stacks/stack-overdue-enrollment/deploy",
      async (route) => {
        deployCalls += 1;
        await route.abort();
      },
    );
    await context.route(
      "**/api/v1/stacks/stack-overdue-enrollment/resume-enrollment",
      async (route) => {
        resumeCalls += 1;
        expect(route.request().method()).toBe("POST");
        expect(route.request().postDataJSON()).toEqual({
          job_id: "job-overdue-enrollment",
          lease_id: "lease-overdue-enrollment",
        });
        await route.fulfill({
          status: 202,
          contentType: "application/json",
          body: JSON.stringify({
            data: {
              success: true,
              message: "Enrollment rollout recovery accepted",
              stack_id: "stack-overdue-enrollment",
              source_job_id: "job-overdue-enrollment",
              lease_id: "lease-overdue-enrollment",
              server_id: "server-overdue-enrollment",
              job_id: "job-overdue-retry",
              idempotent_replay: false,
              provider_vm_create_requested: false,
            },
          }),
        });
      },
    );
    await context.route("**/api/v1/monthly-runtimes", async (route) => {
      if (route.request().method() === "POST") vmCreateCalls += 1;
      await route.fallback();
    });

    const page = await context.newPage();
    await page.clock.install({ time: new Date("2026-07-19T08:05:00Z") });
    await page.goto(
      `${origin}/stacks/creating?name=Waiting%20Stack&job_id=job-overdue-enrollment&stack_id=stack-overdue-enrollment`,
    );

    const recovery = page.getByTestId("waiting-enrollment-recovery");
    await expect(recovery).toBeVisible({ timeout: 15_000 });
    await expect(recovery).toContainText("Stack, Quelljob und exakte Lease");
    await page.getByTestId("waiting-enrollment-retry").click();
    await expect(page).toHaveURL(/job_id=job-overdue-retry/, {
      timeout: 15_000,
    });
    expect(runtimeStatusCalls).toBe(0);
    expect(resumeCalls).toBe(1);
    expect(deployCalls).toBe(0);
    expect(vmCreateCalls).toBe(0);

    await context.close();
  });

  test("uses the short Add Server managed-runtime progress instead of full rollout steps", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await mockLoggedInContext(context, { allowMockAuth: true });
    await mockDiscovery(context);
    await mockJob(context, "job-add-server-running", {
      id: "job-add-server-running",
      type: "update",
      state: "running",
      progress: 25,
      step: "create_lease",
      message: "Managed runtime server requested",
      created: new Date().toISOString(),
      updated: new Date().toISOString(),
      result: {
        creation_operation: "add-server",
        server_provisioning_mode: "kombify-cloud",
        managed_runtime_addition: true,
        stack_id: "stack-add-server",
      },
    });

    const page = await context.newPage();
    await page.goto(
      `${origin}/stacks/creating?operation=add-server&name=techstack-3&job_id=job-add-server-running&stack_id=stack-add-server`,
    );

    await expect(
      page.getByText("Additional server", { exact: true }),
    ).toBeVisible({
      timeout: 15_000,
    });
    await expect(page.getByText("Step 1 of 1")).toBeVisible();
    await expect(
      page.getByText("Managed runtime server requested").first(),
    ).toBeVisible();
    await expect(page.getByTestId("task-group")).toHaveCount(1);
    await expect(
      page.locator('[data-testid="task-group"][data-group-id="provision"]'),
    ).toBeVisible();
    await expect(
      page.locator('[data-testid="task-group"][data-group-id="configure"]'),
    ).toHaveCount(0);
    await expect(page.getByText("techstack-3")).toHaveCount(0);

    await context.close();
  });

  test("completes Add Server managed-runtime request without requiring StackKit handoff", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await mockLoggedInContext(context, { allowMockAuth: true });
    await mockDiscovery(context);
    await mockJob(context, "job-add-server-complete", {
      id: "job-add-server-complete",
      type: "update",
      state: "completed",
      progress: 100,
      step: "create_lease",
      message: "Managed runtime server requested",
      created: new Date().toISOString(),
      updated: new Date().toISOString(),
      result: {
        creation_operation: "add-server",
        server_provisioning_mode: "kombify-cloud",
        managed_runtime_addition: true,
        stack_id: "stack-add-server",
        lease_id: "lease-stack-worker-abcd",
        lease_provider: "centron-managed",
        runtime_offering_id: "monthly-runtime-standard",
        runtime_phase: "lease_requested",
        desired_state: "running",
        billing_mode: "subscription",
      },
    });

    const page = await context.newPage();
    await page.goto(
      `${origin}/stacks/creating?operation=add-server&name=techstack-3&job_id=job-add-server-complete&stack_id=stack-add-server`,
    );

    await expect(
      page.getByRole("heading", {
        name: "Managed server requested",
        level: 2,
      }),
    ).toBeVisible({ timeout: 15_000 });
    await expect(
      page.getByTestId("managed-server-provisioning-card"),
    ).toContainText("Requested");
    await expect(page.getByTestId("runtime-proof-card")).toHaveCount(0);
    await expect(page.getByTestId("stackkit-handoff-missing-card")).toHaveCount(
      0,
    );

    await context.close();
  });

  test("shows verified rollout + lease block when handoff and lease present", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await mockLoggedInContext(context, { allowMockAuth: true });
    await mockDiscovery(context);
    await mockJob(context, "job-ok", {
      id: "job-ok",
      type: "provision",
      state: "completed",
      progress: 100,
      created: new Date().toISOString(),
      updated: new Date().toISOString(),
      result: {
        registration_token: "tok",
        stack_id: "stack-ok",
        server_provisioning_mode: "kombify-cloud",
        runtime_phase: "verified",
        verification_status: "verified",
        e2e_proof: {
          stack_id: "stack-ok",
          rollout_result: "applied",
        },
        lease_id: "lease-ok-1",
        lease_provider: "centron-managed",
        runtime_offering_id: "monthly-runtime-standard",
        runtime_public_ip: "203.0.113.10",
        runtime_ssh_host: "203.0.113.10",
        desired_state: "running",
        billing_mode: "subscription",
        stackkit_outputs: COMPLETE_HANDOFF,
      },
    });

    await seedKombifyCloudConfig(context, origin);
    const page = await context.newPage();
    await page.goto(
      `${origin}/stacks/creating?name=Test%20Stack&job_id=job-ok&stack_id=stack-ok`,
    );

    await expect(
      page.getByRole("heading", {
        name: VERIFIED_ROLLOUT_TITLE,
        level: 2,
      }),
    ).toBeVisible({ timeout: 15_000 });

    const leaseCard = page.getByTestId("managed-server-provisioning-card");
    await expect(leaseCard).toBeVisible();
    await expect(leaseCard).toContainText("lease-ok-1");
    await expect(leaseCard).toContainText("centron-managed");
    await expect(leaseCard).toContainText("203.0.113.10");

    await expect(
      page.getByTestId("stackkit-identity-handoff-card"),
    ).toBeVisible();
    await expect(page.getByTestId("stackkit-owner-identity")).toContainText(
      "owner@example.com",
    );

    await context.close();
  });

  test("reuses existing managed lease when StackKits artifact generation fails", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await context.grantPermissions(["clipboard-read", "clipboard-write"], {
      origin,
    });
    await mockLoggedInContext(context, { allowMockAuth: true });
    await mockDiscovery(context);

    const stackId = "stack-artifact-failed";
    const incidentError =
      'StackKits artifact generation failed: StackKits CLI generate failed: exit status 1: Error: kombified.com registration failed and no subdomainPrefix is configured: auto-register base subdomain: API error 429: {"error":"base subdomain limit reached (max 5 per user)"}';
    await mockJob(context, "job-artifact-failed", {
      id: "job-artifact-failed",
      type: "deploy",
      state: "failed",
      progress: 50,
      step: "generate_iac",
      error: incidentError,
      error_details:
        "Could not generate StackKits rollout artifacts.\n\nBackend error:\n" +
        incidentError,
      created: new Date().toISOString(),
      updated: new Date().toISOString(),
      result: {
        stack_id: stackId,
        server_provisioning_mode: "kombify-cloud",
        runtime_phase: "lease_ready",
        lease_id: "lease-ionos-429",
        lease_provider: "ionos-managed",
        runtime_public_ip: "203.0.113.42",
        runtime_ssh_host: "203.0.113.42",
      },
    });
    await mockJob(context, "job-rollout-retry", {
      id: "job-rollout-retry",
      type: "deploy",
      state: "running",
      progress: 50,
      step: "prepare_rollout",
      message: "Retrying rollout on existing managed VM",
      created: new Date().toISOString(),
      updated: new Date().toISOString(),
      result: {
        stack_id: stackId,
        server_provisioning_mode: "kombify-cloud",
        runtime_phase: "lease_ready",
        lease_id: "lease-ionos-429",
        lease_provider: "ionos-managed",
        runtime_public_ip: "203.0.113.42",
        runtime_ssh_host: "203.0.113.42",
      },
    });

    let runtimeStatusCalls = 0;
    await context.route(
      "**/api/v1/monthly-runtimes/lease-ionos-429",
      async (route) => {
        runtimeStatusCalls += 1;
        await route.abort();
      },
    );

    let retryCalls = 0;
    let deployCalls = 0;
    await context.route(`**/api/v1/stacks/${stackId}/deploy`, async (route) => {
      deployCalls += 1;
      await route.abort();
    });
    await context.route(
      `**/api/v1/stacks/${stackId}/retry-rollout`,
      async (route) => {
        retryCalls += 1;
        expect(route.request().method()).toBe("POST");
        expect(route.request().postDataJSON()).toEqual({
          source_job_id: "job-artifact-failed",
          lease_id: "lease-ionos-429",
        });
        await route.fulfill({
          status: 202,
          contentType: "application/json",
          body: JSON.stringify({
            data: {
              success: true,
              message: "Exact rollout retry accepted",
              stack_id: stackId,
              job_id: "job-rollout-retry",
              source_job_id: "job-artifact-failed",
              lease_id: "lease-ionos-429",
              server_id: "server-ionos-429",
              idempotent_replay: false,
              provider_vm_create_requested: false,
            },
          }),
        });
      },
    );
    let vmCreateCalls = 0;
    await context.route("**/api/v1/monthly-runtimes", async (route) => {
      if (route.request().method() === "POST") vmCreateCalls += 1;
      await route.fallback();
    });

    await seedKombifyCloudConfig(context, origin);
    const page = await context.newPage();
    await page.goto(
      `${origin}/stacks/creating?name=Test%20Stack&job_id=job-artifact-failed&stack_id=${stackId}`,
    );

    await expect(
      page.getByRole("heading", { level: 2, name: "Creation failed" }),
    ).toBeVisible({ timeout: 15_000 });
    await expect(
      page.getByText("StackKit-Artefakte konnten nicht erzeugt werden").first(),
    ).toBeVisible();
    await expect(page.getByText("No matching StackKit found")).toHaveCount(0);

    const provisionGroup = page.locator(
      '[data-testid="task-group"][data-group-id="provision"]',
    );
    await expect(provisionGroup).toHaveAttribute("data-status", "completed");
    const artifactGroup = page.locator(
      '[data-testid="task-group"][data-group-id="generate"]',
    );
    await expect(artifactGroup).toHaveAttribute("data-status", "failed");
    await expect(
      page.locator('[data-task-id="generate_iac"][data-status="failed"]'),
    ).toBeVisible();

    const leaseSummary = page.getByTestId("managed-lease-failure-summary");
    await expect(leaseSummary).toBeVisible();
    await expect(leaseSummary).toContainText(
      "Existing managed VM lease referenced",
    );
    await expect(leaseSummary).toContainText("lease-ionos-429");
    await expect(leaseSummary).toContainText("ionos-managed");
    await expect(leaseSummary).toContainText("203.0.113.42");
    await expect(leaseSummary).toContainText(
      "Only StackKit artifact, domain routing, or later rollout work failed.",
    );

    await expect(page.getByTestId("error-details-text")).toContainText(
      "Could not generate StackKits rollout artifacts.",
    );
    await expect(page.getByTestId("creation-troubleshooting")).toBeVisible();
    await page.getByTestId("copy-error-details-button").click();
    await expect(page.getByTestId("copy-error-details-button")).toContainText(
      "Copied",
    );
    await expect
      .poll(() => page.evaluate(() => navigator.clipboard.readText()))
      .toContain("base subdomain limit reached");

    await page.getByRole("button", { name: "Retry rollout" }).click();
    await expect(page).toHaveURL(/job_id=job-rollout-retry/, {
      timeout: 15_000,
    });
    expect(retryCalls).toBe(1);
    expect(deployCalls).toBe(0);
    expect(vmCreateCalls).toBe(0);
    expect(runtimeStatusCalls).toBe(0);

    await context.close();
  });

  test("surfaces authoritative 503 and 409 rollout retry errors without a lease precheck", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await mockLoggedInContext(context, { allowMockAuth: true });
    await mockDiscovery(context);

    const stackId = "stack-decommissioned";
    await mockJob(context, "job-decommissioned", {
      id: "job-decommissioned",
      type: "deploy",
      state: "failed",
      progress: 50,
      step: "generate_iac",
      error: "StackKits artifact generation failed",
      error_details: "Could not generate StackKits rollout artifacts.",
      created: new Date().toISOString(),
      updated: new Date().toISOString(),
      result: {
        stack_id: stackId,
        server_provisioning_mode: "kombify-cloud",
        runtime_phase: "lease_ready",
        lease_id: "lease-decommissioned",
        lease_provider: "ionos-managed",
        runtime_public_ip: "203.0.113.99",
        runtime_ssh_host: "203.0.113.99",
      },
    });

    let runtimeStatusCalls = 0;
    await context.route(
      "**/api/v1/monthly-runtimes/lease-decommissioned",
      async (route) => {
        runtimeStatusCalls += 1;
        await route.abort();
      },
    );

    let deployCalls = 0;
    let retryCalls = 0;
    await context.route(`**/api/v1/stacks/${stackId}/deploy`, async (route) => {
      deployCalls += 1;
      await route.abort();
    });
    await context.route(
      `**/api/v1/stacks/${stackId}/retry-rollout`,
      async (route) => {
        retryCalls += 1;
        expect(route.request().postDataJSON()).toEqual({
          source_job_id: "job-decommissioned",
          lease_id: "lease-decommissioned",
        });
        const unavailable = retryCalls === 1;
        await route.fulfill({
          status: unavailable ? 503 : 409,
          contentType: "application/json",
          body: JSON.stringify({
            error: {
              code: unavailable ? "retry_unavailable" : "lease_conflict",
              message: unavailable
                ? "Rollout retry is temporarily unavailable"
                : "The exact managed lease is no longer active",
            },
          }),
        });
      },
    );

    await seedKombifyCloudConfig(context, origin);
    const page = await context.newPage();
    await page.goto(
      `${origin}/stacks/creating?name=Test%20Stack&job_id=job-decommissioned&stack_id=${stackId}`,
    );

    await expect(
      page.getByRole("heading", { level: 2, name: "Creation failed" }),
    ).toBeVisible({ timeout: 15_000 });
    await expect(
      page.getByTestId("managed-lease-failure-summary"),
    ).toContainText("lease-decommissioned");

    const retryButton = page.getByRole("button", { name: "Retry rollout" });
    await retryButton.click();
    await expect(
      page.getByText("Rollout retry is temporarily unavailable"),
    ).toBeVisible();
    await retryButton.click();
    await expect(
      page.getByText("The exact managed lease is no longer active"),
    ).toBeVisible();

    expect(runtimeStatusCalls).toBe(0);
    expect(retryCalls).toBe(2);
    expect(deployCalls).toBe(0);

    await context.close();
  });
});
