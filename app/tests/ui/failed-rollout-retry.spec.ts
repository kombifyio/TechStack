import { type BrowserContext, type Route } from "@playwright/test";
import { test, expect } from "./fixtures";
import { mockLoggedInContext } from "../helpers/test-utils";

// A managed rollout that failed after the server was ready (here: the HTTPS
// certificate step) leaves the stack in error, where the backend never reports
// can_start. The Node card's primary button therefore has to be the restart
// that reuses the same server, not a dead Deploy button (owner report
// 2026-10-07).

const stackId = "stack-managed-cert";
const leaseId = "lease-managed-cert";
const failedJobId = "job-origin-certificate-failed";

function apiEnvelope(data: unknown) {
  return {
    data,
    meta: { request_id: "test", timestamp: new Date().toISOString() },
  };
}

async function fulfillJson(route: Route, data: unknown, status = 200) {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(data),
  });
}

const deployment = {
  id: stackId,
  name: "basement",
  mode: "easy",
  status: "error",
  state: "error",
  provider: "ionos-managed",
  server_provisioning_mode: "kombify-cloud",
  lease_id: leaseId,
  services: [],
  created_at: "2026-10-07T09:00:00Z",
  updated_at: "2026-10-07T09:00:00Z",
};

async function mockFailedManagedRollout(context: BrowserContext) {
  await context.route("**/api/v1/features", (route) =>
    fulfillJson(route, apiEnvelope({ security: [], beta: [], ux: [] })),
  );
  await context.route("**/api/v1/stacks", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await fulfillJson(route, apiEnvelope([deployment]));
  });
  await context.route("**/api/v1/homelab", (route) =>
    fulfillJson(
      route,
      apiEnvelope({
        homelab: { id: "hl-1", name: "My Homelab", named: true, intent: {} },
        kit_deployments: [deployment],
      }),
    ),
  );
  await context.route("**/api/v1/workers", (route) =>
    fulfillJson(route, apiEnvelope([])),
  );
  await context.route("**/api/v1/jobs**", (route) =>
    fulfillJson(route, apiEnvelope({ items: [] })),
  );
  await context.route("**/api/v1/services**", (route) =>
    fulfillJson(route, apiEnvelope([])),
  );
  await context.route("**/api/v1/servers", (route) =>
    fulfillJson(
      route,
      apiEnvelope([
        {
          id: "srv-managed",
          node_id: "node-managed",
          kit_deployment_id: stackId,
          name: "managed-box",
          lifecycle: { state: "active", desired_state: "active" },
          connection: {
            state: "connected",
            changed_at: new Date().toISOString(),
          },
          health: { state: "healthy" },
          channels: [],
          inventory_revision: 1,
          provider: {},
          mutations_allowed: true,
        },
      ]),
    ),
  );
  await context.route(`**/api/v1/stacks/${stackId}/operations`, (route) =>
    fulfillJson(
      route,
      apiEnvelope({
        stack: {
          id: stackId,
          name: "basement",
          status: "error",
          state: "error",
        },
        readiness: {
          status: "error",
          can_start: false,
          required_servers: 1,
          approved_servers: 1,
          connected_servers: 1,
          message: "The last rollout failed",
        },
        nextSteps: [],
        kpis: {
          registered_servers: 1,
          healthy_servers: 1,
          running_services: 0,
        },
        servers: [
          {
            id: "srv-managed",
            server_id: "srv-managed",
            hostname: "managed-box",
            role: "server",
            status: "online",
            assignment: "stack",
            kit_deployment_id: stackId,
            agent_id: "agent-managed",
            approved: true,
            precheck_state: "passed",
            source: "managed-runtime",
            health: { state: "healthy" },
            lease_id: leaseId,
            capabilities: {},
          },
        ],
        services: [],
        monitoring: { status: "unknown", alerts: [] },
        alerts: [],
        latestFailure: {
          job_id: failedJobId,
          type: "deploy",
          state: "failed",
          step: "origin_certificate",
          lease_id: leaseId,
          reason: "typed_stackkit_origin_certificate_failed",
          message: "The HTTPS certificate could not be installed",
          diagnostics_available: false,
        },
      }),
    ),
  );
}

test("a failed managed rollout offers an enabled primary restart on its server", async ({
  context,
  baseURL,
}) => {
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockFailedManagedRollout(context);

  const retryBodies: unknown[] = [];
  let deployCalls = 0;
  await context.route(`**/api/v1/stacks/${stackId}/deploy`, async (route) => {
    deployCalls += 1;
    await route.abort();
  });
  await context.route(
    `**/api/v1/stacks/${stackId}/retry-rollout`,
    async (route) => {
      retryBodies.push(route.request().postDataJSON());
      await fulfillJson(
        route,
        apiEnvelope({
          success: true,
          message: "Exact rollout retry accepted",
          stack_id: stackId,
          job_id: "job-rollout-retry",
          source_job_id: failedJobId,
          lease_id: leaseId,
          provider_vm_create_requested: false,
        }),
        202,
      );
    },
  );

  const page = await context.newPage();
  await page.goto(`${baseURL}/dashboard`);

  // Node actions live in the card's menu, which opens above the list.
  await page.getByTestId("node-actions-toggle").click();
  const restart = page.getByTestId("retry-rollout-button");
  await expect(restart).toBeVisible();
  await expect(restart).toBeEnabled();
  // The dead Deploy button is replaced, not shown beside it.
  await expect(page.getByTestId("deploy-stackkit-button")).toHaveCount(0);

  await restart.click();

  await expect(page).toHaveURL(/job_id=job-rollout-retry/, { timeout: 15_000 });
  expect(retryBodies).toEqual([
    { source_job_id: failedJobId, lease_id: leaseId },
  ]);
  expect(deployCalls).toBe(0);
});
