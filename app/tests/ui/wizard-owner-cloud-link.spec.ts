import { type BrowserContext } from "@playwright/test";
import { test, expect } from "./fixtures";
import {
  chooseOwnerSource,
  mockLoggedInContext,
  requireAppBase,
  walkEasyWizardToOwnerStep,
} from "../helpers/test-utils";

type WizardRunOwner = {
  owner_bootstrap_mode?: string;
  owner_source?: string;
  owner_email?: string;
  owner_username?: string;
  owner_display_name?: string;
};

const LINKED_STATUS = {
  linked: true,
  external_email: "linked.owner@example.com",
  external_name: "Linked Owner",
  email_verified: true,
  linked_at: "2026-07-05 10:00:00.000Z",
};

async function mockSelfHostedAuthMode(context: BrowserContext) {
  await context.route("**/api/v1/auth/mode", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        mode: "local",
        deployment_mode: "self-hosted",
        is_first_run: false,
        cloud_auth_url: null,
        portal_url: null,
        allow_local_login: true,
      }),
    });
  });
}

function mockCloudLinkStatus(
  context: BrowserContext,
  getStatus: () => Record<string, unknown>,
) {
  return context.route("**/api/v1/auth/cloud-link/status", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: getStatus() }),
    });
  });
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

/** Answer the wizard run and its provision job; returns the submitted owner. */
async function mockWizardRun(context: BrowserContext) {
  const submitted: { owner?: WizardRunOwner } = {};
  await context.route("**/api/v1/wizard/runs", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    submitted.owner = route.request().postDataJSON().owner ?? {};
    await route.fulfill({
      status: 202,
      json: {
        data: {
          run_id: "run_cloud_link",
          run_kind: "first-run",
          homelab_id: "hl_cloud_link",
          kit_assignment_mode: "found",
          stack_id: "stack_cloud_link",
          name: "homelab",
          job_id: "job_cloud_link",
          state: "provisioning",
        },
      },
    });
  });
  await context.route("**/api/v1/jobs/job_cloud_link**", async (route) => {
    await route.fulfill({
      json: {
        data: {
          id: "job_cloud_link",
          type: "provision",
          state: "running",
          progress: 10,
          step: "validate",
          result: {},
        },
      },
    });
  });
  return submitted;
}

async function openOwnerStep(context: BrowserContext, origin: string) {
  const page = await context.newPage();
  await page.goto(`${origin}/stacks/new`);
  await walkEasyWizardToOwnerStep(page, {
    serverProvisioning: "install-command",
    access: "home",
  });
  return page;
}

async function prepare(context: BrowserContext, baseURL: string | undefined) {
  const origin = requireAppBase(baseURL);
  await context.grantPermissions(["notifications"], { origin });
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockSelfHostedAuthMode(context);
  await mockDiscovery(context);
  return origin;
}

test.describe("Wizard cloud-linked owner", () => {
  test("linked profile submits owner_source cloud-linked without identity fields", async ({
    context,
    baseURL,
  }) => {
    const origin = await prepare(context, baseURL);
    await mockCloudLinkStatus(context, () => LINKED_STATUS);
    const submitted = await mockWizardRun(context);

    const page = await openOwnerStep(context, origin);
    await chooseOwnerSource(page, "cloud-linked");
    await expect(page.getByTestId("cloud-link-status")).toContainText(
      "linked.owner@example.com",
    );

    await page.getByTestId("wizard-create").click();
    await expect.poll(() => submitted.owner).toBeDefined();
    expect(submitted.owner).toMatchObject({ owner_source: "cloud-linked" });
    expect(submitted.owner?.owner_email).toBeUndefined();
    expect(submitted.owner?.owner_username).toBeUndefined();
    expect(submitted.owner?.owner_display_name).toBeUndefined();
  });

  test("unlinked profile offers connect and picks up the link via polling", async ({
    context,
    baseURL,
  }) => {
    const origin = await prepare(context, baseURL);
    let linked = false;
    await mockCloudLinkStatus(context, () =>
      linked ? LINKED_STATUS : { linked: false },
    );
    await context.route("**/api/v1/auth/cloud-link/start", async (route) => {
      await route.fulfill({
        json: {
          data: {
            // about:blank keeps the popup inert; the card's status polling is
            // the completion signal under test here.
            authorization_url: "about:blank",
            expires_at: "2099-07-05T10:10:00Z",
          },
        },
      });
    });

    const page = await openOwnerStep(context, origin);
    await chooseOwnerSource(page, "cloud-linked");
    await page.getByTestId("cloud-link-connect").click();

    // The backend completes the link out-of-band; polling picks it up.
    linked = true;
    await expect(page.getByTestId("cloud-link-status")).toContainText(
      "linked.owner@example.com",
      { timeout: 10000 },
    );
    await expect(
      page.getByTestId("owner-source-cloud-linked-select"),
    ).toBeChecked();
  });

  test("owner bootstrap denial shows the backend reason", async ({
    context,
    baseURL,
  }) => {
    const origin = await prepare(context, baseURL);
    await mockCloudLinkStatus(context, () => LINKED_STATUS);
    await context.route("**/api/v1/wizard/runs", async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      await route.fulfill({
        status: 403,
        json: {
          error: {
            code: "FORBIDDEN",
            message:
              "No linked kombify Cloud profile is available for the cloud-linked owner",
            details: {
              phase: "owner_bootstrap",
              error_code: "owner_bootstrap_denied",
              reason_code: "cloud_link_missing",
              retryable: true,
            },
          },
        },
      });
    });

    const page = await openOwnerStep(context, origin);
    await chooseOwnerSource(page, "cloud-linked");
    await page.getByTestId("wizard-create").click();

    await expect(
      page
        .getByText(
          "No linked kombify Cloud profile is available for the cloud-linked owner",
        )
        .first(),
    ).toBeVisible({ timeout: 15000 });
  });
});
