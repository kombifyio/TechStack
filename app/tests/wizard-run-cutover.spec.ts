import { test, expect, type BrowserContext } from "@playwright/test";
import { mockLoggedInContext, requireAppBase } from "./helpers/test-utils";

// hio.5: with the native_v2_wizard flag on, the wizard submits one wizard-run
// request (POST /api/v1/wizard/runs) and lands directly on the progress page;
// the dashboard renders a resume banner while a run is active.

const featureLabels: Record<string, string> = {
  native_v2_wizard: "Native v2 Wizard",
  monthly_runtime: "Monthly Runtime",
  monthly_runtime_cloudkit: "Monthly Runtime Cloud Kit",
  monthly_runtime_centron: "Monthly Runtime Centron",
  monthly_runtime_ionos: "Monthly Runtime IONOS",
};

async function mockFeatures(context: BrowserContext, enabledKeys: string[]) {
  await context.route("**/api/v1/features", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          security: [],
          beta: Object.entries(featureLabels).map(([key, name]) => ({
            key,
            name,
            enabled: enabledKeys.includes(key),
            locked: false,
            requires_consent: false,
            has_consent: false,
            risk_level: "high",
            description: "",
            category: "beta",
          })),
          ux: [],
        },
      }),
    });
  });
}

async function mockCsrf(context: BrowserContext) {
  await context.route("**/api/v1/csrf**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ token: "test-csrf-token" }),
    });
  });
}

async function mockCloudAuth(context: BrowserContext) {
  await context.route("**/api/v1/auth/mode", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          mode: "cloud",
          deployment_mode: "saas",
          is_first_run: false,
          cloud_auth_url: null,
          portal_url: "https://kombify.io",
          allow_local_login: false,
        },
      }),
    });
  });
  await context.route("**/api/v2/auth/providers", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ providers: [] }),
    });
  });
  await context.route("**/api/v2/whoami", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        subject: "cloud-user-1",
        tenantId: "tenant-1",
        email: "admin@example.com",
        role: "owner",
      }),
    });
  });
}

test("flag on: easy wizard submits a wizard run and lands on the progress page", async ({
  browser,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  const context = await browser.newContext();
  await context.grantPermissions(["notifications"], { origin });
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockFeatures(context, Object.keys(featureLabels));
  await mockCloudAuth(context);
  await mockCsrf(context);

  let runRequest: Record<string, any> | null = null;
  await context.route("**/api/v1/wizard/runs", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    runRequest = route.request().postDataJSON();
    await route.fulfill({
      status: 202,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          run_id: "run-1",
          run_kind: "first-run",
          requested_run_kind: "first-run",
          coerced: false,
          homelab_id: "hl-1",
          kit_assignment_mode: "found",
          kit_slug: "cloud-kit",
          stack_id: "stack_1",
          server_id: "server_1",
          node_id: "main",
          name: "homelab",
          job_id: "job_1",
          state: "provisioning",
          auto_deploy: true,
          operations_url: "/stacks?stack_id=stack_1",
        },
      }),
    });
  });
  // The creating page polls the provision job (SSE falls back on 404).
  await context.route("**/api/v1/jobs/job_1/stream**", async (route) => {
    await route.fulfill({ status: 404, body: "" });
  });
  await context.route("**/api/v1/jobs/job_1**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          id: "job_1",
          type: "provision",
          state: "running",
          progress: 40,
          step: "create_spec",
          message: "Provisioning",
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        },
      }),
    });
  });

  const page = await context.newPage();
  await page.goto(`${origin}/stacks/new`);
  await page.getByTestId("hydrated").waitFor({ state: "attached" });

  await expect(page.getByTestId("easy-step-1")).toBeVisible();
  await page.getByTestId("easy-feature-storage").check();
  await page.getByTestId("wizard-next").click();
  await expect(page.getByTestId("easy-step-2")).toBeVisible();
  await page.getByTestId("wizard-next").click();
  await expect(page.getByTestId("easy-step-3")).toBeVisible();
  await page.getByTestId("easy-access-anywhere").click();
  await page.getByTestId("wizard-next").click();
  await expect(page.getByTestId("easy-step-4")).toBeVisible();
  await page.getByTestId("easy-users-me").check();
  await page.getByTestId("wizard-next").click();
  await expect(page.getByTestId("easy-step-5")).toBeVisible();

  const runRequestPromise = page.waitForRequest(
    (req) =>
      req.url().includes("/api/v1/wizard/runs") && req.method() === "POST",
  );
  await page.getByTestId("wizard-create").click();
  const observedRunRequest = await runRequestPromise;
  runRequest ??= observedRunRequest.postDataJSON();

  // The closed intent contract travels; the legacy stack_spec does not.
  expect(runRequest).toBeTruthy();
  expect(runRequest!.intent?.schema).toBe("techstack.wizard-intent/v1");
  expect(runRequest!.intent?.run_kind).toBe("first-run");
  expect(runRequest!.intent?.name).toBe("homelab");
  expect(runRequest!.intent?.kit_assignment).toMatchObject({
    mode: "found",
    kit_slug: "cloud-kit",
  });
  expect(runRequest!.intent?.server?.transport).toBe("kombify-cloud");
  expect(runRequest!.managed?.provider_id).toBeTruthy();
  expect(runRequest!.stack_spec).toBeUndefined();

  // Direct handoff to the progress page (plan D6) — no creation=1 detour.
  await page.waitForURL("**/stacks/creating**");
  expect(page.url()).toContain("job_id=job_1");
  expect(page.url()).toContain("stack_id=stack_1");

  await context.close();
});

test("dashboard shows the resume banner while a wizard run is active", async ({
  browser,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  const context = await browser.newContext();
  await context.grantPermissions(["notifications"], { origin });
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockFeatures(context, ["native_v2_wizard"]);
  await mockCloudAuth(context);
  await mockCsrf(context);

  await context.route("**/api/v1/wizard/runs/active", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          run: {
            run_id: "run-1",
            status: "completed",
            run_kind: "first-run",
            homelab_id: "hl-1",
            stack_id: "stack_1",
            node_id: "main",
            job_id: "job_1",
            result: {
              name: "homelab",
              state: "provisioning",
              kit_assignment_mode: "found",
            },
            job: {
              id: "job_1",
              state: "running",
              progress: 55,
              step: "create_spec",
              message: "Provisioning your homelab",
            },
          },
        },
      }),
    });
  });
  await context.route("**/api/v1/stacks**", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    });
  });
  await context.route("**/api/v1/workers**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    });
  });
  await context.route("**/api/v1/inventory/servers**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { servers: [] } }),
    });
  });
  await context.route("**/api/v1/homelab", async (route) => {
    await route.fulfill({
      status: 404,
      contentType: "application/json",
      body: JSON.stringify({
        error: { code: "NOT_FOUND", message: "no homelab" },
      }),
    });
  });

  const page = await context.newPage();
  await page.goto(`${origin}/stacks`);

  const banner = page.getByTestId("wizard-run-banner");
  await expect(banner).toBeVisible();
  await expect(banner).toContainText("Provisioning your homelab");
  const resume = page.getByTestId("wizard-run-banner-resume");
  await expect(resume).toHaveAttribute("href", /\/stacks\/creating\?/);
  await expect(resume).toHaveAttribute("href", /job_id=job_1/);

  await context.close();
});
