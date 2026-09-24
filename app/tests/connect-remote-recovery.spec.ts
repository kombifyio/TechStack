import { test, expect, type BrowserContext, type Page, type Route } from "@playwright/test";
import { mockLoggedInContext, requireAppBase } from "./helpers/test-utils";

function apiEnvelope(data: unknown) {
  return {
    data,
    meta: {
      request_id: "test",
      timestamp: "2026-05-18T09:00:00Z",
    },
  };
}

async function fulfillJson(route: Route, data: unknown, status = 200) {
  const origin =
    route.request().headers()["origin"] ??
    process.env.PLAYWRIGHT_BASE_URL ??
    "http://127.0.0.1:5261";
  await route.fulfill({
    status,
    headers: {
      "access-control-allow-headers": "*",
      "access-control-allow-methods": "GET,POST,OPTIONS",
      "access-control-allow-origin": origin,
      "access-control-allow-credentials": "true",
      "content-type": "application/json",
    },
    body: JSON.stringify(data),
  });
}

function canonicalServer(
  stackId: string,
  heartbeatAt: string,
  overrides: Record<string, unknown> = {},
) {
  return {
    id: "node-remote-1",
    node_id: "node-remote-1",
    kit_deployment_id: stackId,
    name: "remote-node-1",
    worker_id: "guard-remote-1",
    lifecycle: { state: "active", desired_state: "running" },
    connection: {
      state: "connected",
      changed_at: heartbeatAt,
      last_heartbeat_at: heartbeatAt,
      staleness_seconds: 0,
    },
    health: { state: "healthy", observed_at: heartbeatAt },
    channels: [],
    inventory_revision: 1,
    provider: {},
    environment_class: "local",
    offering: "self_owned_device",
    availability_owner: "customer",
    operations_owner: "customer",
    target_evidence: {
      ref: "guard:guard-remote-1",
      observed_at: heartbeatAt,
      freshness: { state: "recorded", age_seconds: 0 },
    },
    mutations_allowed: true,
    created_at: heartbeatAt,
    updated_at: heartbeatAt,
    ...overrides,
  };
}

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
  const payload = JSON.stringify({ data: body });
  await context.route(`**/api/v1/jobs/${jobId}`, async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: payload,
    });
  });
}

async function seedConnectRemoteConfig(context: BrowserContext, origin: string) {
  const page = await context.newPage();
  await page.goto(`${origin}/stacks/creating`);
  await page.evaluate(() => {
    sessionStorage.setItem(
      "creatingStackConfig",
      JSON.stringify({
        serverProvisioning: { mode: "connect-remote" },
      }),
    );
    sessionStorage.setItem("creatingStackName", "Remote Stack");
  });
  await page.close();
}

async function mockDashboardApi(page: Page, stackId: string) {
  await page.addInitScript(() => {
    const payload = btoa(
      JSON.stringify({
        id: "owner-1",
        exp: Math.floor(Date.now() / 1000) + 3600,
      }),
    )
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/g, "");
    window.localStorage.setItem(
      "pocketbase_auth",
      JSON.stringify({
        token: `test.${payload}.signature`,
        model: {
          id: "owner-1",
          email: "owner@example.com",
          collectionName: "users",
        },
      }),
    );
  });

  await page.route("**/api/v1/auth/mode", async (route) => {
    await fulfillJson(
      route,
      apiEnvelope({
        mode: "local",
        deployment_mode: "self-hosted",
        is_first_run: false,
        allow_local_login: true,
      }),
    );
  });

  await page.route("**/api/v2/whoami", async (route) => {
    await fulfillJson(route, {
      user: { id: "owner-1", email: "owner@example.com", name: "Owner" },
      source: "local",
    });
  });

  await page.route("**/api/v1/features", async (route) => {
    await fulfillJson(route, apiEnvelope({ security: [], beta: [], ux: [] }));
  });

  // Every mutation fetches a CSRF token first; unmocked, the no-setup backend
  // rejects it cross-origin and the retry POST is never sent.
  await page.route("**/api/v1/csrf**", async (route) => {
    await fulfillJson(route, { token: "test-csrf-token" });
  });

  await page.route("**/api/v1/tunnel/registry-url", async (route) => {
    await fulfillJson(
      route,
      apiEnvelope({ url: "http://localhost:5260", mode: "local" }),
    );
  });

  await page.route("**/api/v1/stacks", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await fulfillJson(
      route,
      apiEnvelope([
        {
          id: stackId,
          name: "Remote Stack",
          mode: "easy",
          status: "error",
          state: "error",
          provider: "local",
          server_provisioning_mode: "connect-remote",
          services: ["pocket_id"],
          created_at: "2026-05-18T09:00:00Z",
          updated_at: "2026-05-18T09:00:00Z",
        },
      ]),
    );
  });

  await page.route("**/api/v1/homelab", async (route) => {
    await fulfillJson(
      route,
      apiEnvelope({
        homelab: {
          id: "hl-1",
          name: "My Homelab",
          intent: {},
          created: "2026-05-18T09:00:00Z",
          updated: "2026-05-18T09:00:00Z",
        },
        kit_deployments: [
          {
            id: stackId,
            name: "Remote Stack",
            mode: "easy",
            status: "error",
            state: "error",
            provider: "local",
            server_provisioning_mode: "connect-remote",
            services: ["pocket_id"],
            created_at: "2026-05-18T09:00:00Z",
            updated_at: "2026-05-18T09:00:00Z",
          },
        ],
      }),
    );
  });

  await page.route("**/api/v1/workers", async (route) => {
    await fulfillJson(route, apiEnvelope([]));
  });

  await page.route("**/api/v1/inventory/servers**", async (route) => {
    await fulfillJson(route, apiEnvelope({ servers: [] }));
  });

  await page.route("**/api/v1/jobs**", async (route) => {
    await fulfillJson(route, apiEnvelope({ items: [] }));
  });
}

test.describe("connect-remote decoupled recovery", () => {
  test("shows StackKit-only retry when SSH enrollment succeeded but provision failed", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await mockLoggedInContext(context, { allowMockAuth: true });
    await mockDiscovery(context);

    const stackId = "stack-connect-remote";
    const heartbeatAt = new Date().toISOString();
    await context.route("**/api/v1/servers**", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [canonicalServer(stackId, heartbeatAt)],
        }),
      });
    });

    await mockJob(context, "job-remote-enrolled", {
      id: "job-remote-enrolled",
      type: "remote_enrollment",
      state: "completed",
      progress: 100,
      step: "remote_ssh_enrolled",
      message: "Guard enrolled over SSH",
      result: {
        stack_id: stackId,
        server_provisioning_mode: "connect-remote",
        server_remote_host: "82.165.251.178",
      },
    });
    await mockJob(context, "job-provision-failed", {
      id: "job-provision-failed",
      type: "provision",
      state: "failed",
      progress: 40,
      step: "prepare_rollout",
      error: "StackKit preparation failed after SSH enrollment",
      created: heartbeatAt,
      updated: heartbeatAt,
      result: {
        stack_id: stackId,
        server_provisioning_mode: "connect-remote",
      },
    });

    let provisionRequests = 0;
    await context.route(`**/api/v1/stacks/${stackId}/provision`, async (route) => {
      provisionRequests += 1;
      await route.fulfill({
        status: 202,
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            success: true,
            message: "Provisioning started",
            job_id: "job-provision-retry",
          },
        }),
      });
    });

    await seedConnectRemoteConfig(context, origin);
    const page = await context.newPage();
    await page.goto(
      `${origin}/stacks/creating?stack_id=${stackId}&job_id=job-provision-failed&pairing_job_id=job-remote-enrolled&name=Remote%20Stack`,
    );

    await expect(
      page.getByRole("heading", { level: 2, name: "Creation failed" }),
    ).toBeVisible({ timeout: 15_000 });

    const summary = page.getByTestId("connect-remote-stackkit-failure-summary");
    await expect(summary).toBeVisible();
    await expect(summary).toContainText("Node connection succeeded");
    await expect(summary).toContainText("without repeating SSH enrollment");

    await page
      .getByRole("button", { name: "Continue StackKit on connected Node" })
      .click();
    await expect(page).toHaveURL(/job_id=job-provision-retry/, {
      timeout: 15_000,
    });
    expect(provisionRequests).toBe(1);

    await context.close();
  });

  test("dashboard retries remote_enrollment through resume-remote-enrollment", async ({
    page,
  }) => {
    const stackId = "stack-remote-dashboard";
    await mockDashboardApi(page, stackId);

    await page.route(`**/api/v1/stacks/${stackId}/operations`, async (route) => {
      await fulfillJson(
        route,
        apiEnvelope({
          stack: {
            id: stackId,
            name: "Remote Stack",
            status: "error",
            state: "error",
          },
          readiness: {
            status: "error",
            can_start: false,
            required_servers: 1,
            approved_servers: 0,
            connected_servers: 0,
            message: "SSH enrollment failed",
          },
          nextSteps: [],
          kpis: {
            registered_servers: 0,
            healthy_servers: 0,
            running_services: 0,
          },
          servers: [],
          services: [],
          monitoring: { status: "unknown", alerts: [] },
          alerts: [],
          latestFailure: {
            job_id: "job-remote-enrollment-failed",
            type: "remote_enrollment",
            state: "failed",
            step: "remote_ssh_connect",
            message: "SSH authentication failed",
            error: "ssh: handshake failed",
            diagnostics_available: false,
          },
        }),
      );
    });

    let resumeRequests = 0;
    await page.route(
      `**/api/v1/stacks/${stackId}/resume-remote-enrollment`,
      async (route) => {
        resumeRequests += 1;
        await fulfillJson(
          route,
          apiEnvelope({
            success: true,
            message: "Remote enrollment retry accepted",
            kit_deployment_id: stackId,
            pairing_job_id: "job-remote-enrollment-retry",
          }),
          202,
        );
      },
    );

    await page.goto(`/stacks?stack_id=${stackId}&creation=1`);
    await page
      .getByTestId("latest-failure-collapsible")
      .getByRole("button", { name: /SSH connection to your Node failed/ })
      .click();
    await page
      .getByRole("button", { name: "SSH-Verbindung erneut versuchen" })
      .click();

    await expect.poll(() => resumeRequests).toBe(1);
    await expect(page).toHaveURL(/pairing_job_id=job-remote-enrollment-retry/);
  });
});
