import { type BrowserContext } from "@playwright/test";
import { test, expect } from "./fixtures";
import {
  mockLoggedInContext,
  selectUseCase,
  requireAppBase,
} from "../helpers/test-utils";

const managedRuntimeFeatureLabels: Record<string, string> = {
  monthly_runtime: "Monthly Runtime",
  monthly_runtime_cloudkit: "Monthly Runtime Cloud Kit",
  monthly_runtime_centron: "Monthly Runtime Centron",
  monthly_runtime_ionos: "Monthly Runtime IONOS",
};

async function mockFeatureEntitlements(
  context: BrowserContext,
  enabledKeys: string[],
) {
  await context.route("**/api/v1/features", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          security: [],
          beta: Object.entries(managedRuntimeFeatureLabels).map(
            ([key, name]) => ({
              key,
              name,
              enabled: enabledKeys.includes(key),
              locked: false,
              requires_consent: false,
              has_consent: false,
              risk_level: "high",
              description: "",
              category: "beta",
            }),
          ),
          ux: [],
        },
      }),
    });
  });
}

test("easy wizard explains disabled managed runtime entitlements", async ({
  context,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);

  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockFeatureEntitlements(context, []);
  const page = await context.newPage();

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
        role: "member",
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

  await page.goto(`${origin}/stacks/new`);
  await page.getByTestId("hydrated").waitFor({ state: "attached" });
  await selectUseCase(page, "storage");
  await page.getByTestId("wizard-next").click();

  await page.getByTestId("server-branch-new").click();
  const managedMode = page.getByTestId("server-mode-kombify-cloud");
  await expect(managedMode).toBeDisabled();
  await expect(managedMode).toHaveAttribute("aria-pressed", "false");
  await expect(page.getByTestId("wizard-next")).toBeDisabled();
  await expect(page.getByTestId("managed-runtime-unavailable")).toContainText(
    /Monthly Runtime.*Cloud Kit/,
  );
  await expect(page.getByTestId("managed-provider-selector")).not.toBeVisible();

  await page.getByTestId("server-branch-owned").click();
  await page.getByTestId("server-mode-install-command").click();
  await expect(page.getByTestId("wizard-next")).toBeEnabled();
});

test("easy wizard retains the chosen Proxmox system while exploring other paths", async ({
  context,
  baseURL,
}) => {
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockFeatureEntitlements(context, []);
  const page = await context.newPage();
  await page.goto(`${requireAppBase(baseURL)}/stacks/new`);
  await page.getByTestId("hydrated").waitFor({ state: "attached" });
  await selectUseCase(page, "storage");
  await page.getByTestId("wizard-next").click();
  await page.getByTestId("server-branch-owned").click();
  await page.getByTestId("server-mode-install-command").click();
  await page.getByRole("button", { name: /^Ubuntu \/ Linux/ }).click();
  await page.getByTestId("server-mode-hypervisor").check();

  // Re-selecting the current path must not turn a Proxmox host into a Linux Node.
  await page.getByTestId("server-mode-install-command").click();
  await expect(page.getByTestId("server-mode-hypervisor")).toBeChecked();
  await page.getByTestId("server-branch-new").click();
  await page.getByTestId("server-branch-owned").click();
  await expect(page.getByTestId("server-mode-hypervisor")).toBeChecked();
  await expect(page.getByTestId("wizard-next")).toBeDisabled();
});

test("easy wizard shows both managed providers but only enables account grants", async ({
  context,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);

  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockFeatureEntitlements(context, [
    "monthly_runtime",
    "monthly_runtime_cloudkit",
    "monthly_runtime_ionos",
  ]);
  const page = await context.newPage();

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
        subject: "cloud-owner-1",
        tenantId: "tenant-1",
        email: "owner@example.com",
        role: "owner",
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

  await page.goto(`${origin}/stacks/new`);
  await page.getByTestId("hydrated").waitFor({ state: "attached" });
  await selectUseCase(page, "storage");
  await page.getByTestId("wizard-next").click();

  await page.getByTestId("server-branch-new").click();
  const managedMode = page.getByTestId("server-mode-kombify-cloud");
  await expect(managedMode).toBeEnabled();
  await managedMode.click();
  await expect(managedMode).toHaveAttribute("aria-pressed", "true");
  await expect(
    page.getByTestId("managed-runtime-unavailable"),
  ).not.toBeVisible();
  await expect(
    page.getByTestId("managed-provider-centron").locator('input[type="radio"]'),
  ).toBeDisabled();
  await page.getByTestId("managed-provider-centron").click({ force: true });
  await expect(
    page.getByTestId("managed-provider-ionos").locator('input[type="radio"]'),
  ).toBeChecked();
});
