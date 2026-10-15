import { type Route } from "@playwright/test";
import { test, expect } from "./fixtures";
import { mockLoggedInContext } from "../helpers/test-utils";

// Owner direction 2026-10-10: Applications are what a user opens through a UI;
// the Apps page lists them as compact cards or logos only, and a logo opens
// the application. System services stay on the Services page.

function fulfillJson(route: Route, data: unknown) {
  return route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify({
      data,
      meta: { request_id: "test", timestamp: new Date().toISOString() },
    }),
  });
}

function service(
  id: string,
  key: string,
  name: string,
  url?: string,
  extra: Record<string, unknown> = {},
) {
  return {
    id,
    kit_deployment_id: "stack-1",
    server_id: "srv-1",
    target_kind: "server",
    placement: { freshness: { state: "recorded" } },
    service_key: key,
    application_key: key,
    service_instance: "default",
    name,
    management_state: "observed",
    desired_state: "running",
    observed_state: "running",
    health: { state: "healthy" },
    access: url ? { mode: "direct", url } : {},
    allowed_actions: [],
    inventory_revision: 1,
    source: "guard",
    provenance: {},
    created_at: "2026-10-07T09:00:00Z",
    updated_at: "2026-10-09T08:00:00Z",
    ...extra,
  };
}

test("apps page lists applications as cards or logos and a logo opens the app", async ({
  context,
  baseURL,
}) => {
  await mockLoggedInContext(context, { allowMockAuth: true });
  await context.route("**/api/v1/features", (route) =>
    fulfillJson(route, { security: [], beta: [], ux: [] }),
  );
  await context.route("**/api/v1/services**", (route) =>
    fulfillJson(route, [
      service("svc-immich", "immich", "immich", "https://photos.home.example", {
        management_state: "managed",
        placement_class: "local",
        allowed_actions: ["restart", "logs"],
      }),
      service("svc-hermes", "hermes", "Hermes", "https://hermes.home.example", {
        placement_class: "cloud",
        allowed_actions: ["adopt"],
      }),
      service("svc-traefik", "traefik", "Traefik"),
    ]),
  );
  const page = await context.newPage();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`${baseURL}/apps`);

  await page.getByTestId("apps-view-list").click();
  const cards = page.getByTestId("apps-list").getByTestId("application-card");
  await expect(cards).toHaveCount(2);
  await expect(page.getByTestId("apps-page")).not.toContainText("Traefik");

  await page.getByTestId("apps-view-logos").click();
  const immich = page
    .getByTestId("apps-logos")
    .locator('[data-service-id="svc-immich"]');
  await expect(immich).toHaveAttribute("href", "https://photos.home.example");
  const shot = process.env.NODE_CARDS_SHOTS;
  if (shot) await page.screenshot({ path: `${shot}/apps-logos-mobile.png` });
  await page.getByTestId("apps-view-list").click();
  await expect(page.getByTestId("apps-view-list")).toHaveAttribute(
    "aria-selected",
    "true",
  );
  if (shot) await page.screenshot({ path: `${shot}/apps-list-mobile.png` });
  await page.getByTestId("apps-view-cards").click();
  if (shot) {
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.screenshot({ path: `${shot}/apps-cards-desktop.png` });
  }
});

// Owner direction 2026-10-10: a stacked app offers Restart right on its card
// behind the owner approval; an unstacked one offers "Stack it" instead.
test("a stacked app restarts from its card after approval, an unstacked one offers Stack it", async ({
  context,
  baseURL,
}) => {
  await mockLoggedInContext(context, { allowMockAuth: true });
  await context.route("**/api/v1/features", (route) =>
    fulfillJson(route, { security: [], beta: [], ux: [] }),
  );
  await context.route("**/api/v1/services**", (route) =>
    fulfillJson(route, [
      service("svc-immich", "immich", "immich", "https://photos.home.example", {
        management_state: "managed",
        allowed_actions: ["restart", "backup"],
      }),
      service("svc-hermes", "hermes", "Hermes", "https://hermes.home.example", {
        allowed_actions: ["adopt"],
      }),
    ]),
  );
  let restart: Record<string, unknown> | undefined;
  let backup: Record<string, unknown> | undefined;
  await context.route(
    "**/api/v1/registry/services/svc-immich/actions",
    async (route) => {
      const body = route.request().postDataJSON();
      if (body.action === "backup") backup = body;
      else restart = body;
      await route.fulfill({
        status: 202,
        json: {
          data: {
            job_id: "job-restart-1",
            service_id: "svc-immich",
            action: "restart",
            status: "queued",
          },
        },
      });
    },
  );
  await context.route("**/api/v1/jobs/job-restart-1", (route) =>
    route.fulfill({
      json: { data: { id: "job-restart-1", state: "running", progress: 10 } },
    }),
  );
  const page = await context.newPage();
  await page.goto(`${baseURL}/apps`);
  await page.getByTestId("apps-view-list").click();

  const list = page.getByTestId("apps-list");
  await list
    .locator('[data-service-id="svc-immich"]')
    .getByRole("button", { name: /^Restart/ })
    .click();
  expect(restart).toBeUndefined();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Approve restart" })
    .click();
  await expect
    .poll(() => restart)
    .toMatchObject({ action: "restart", owner_approved: true });

  // Back up now sits next to Restart and runs behind the same approval.
  await list
    .locator('[data-service-id="svc-immich"]')
    .getByRole("button", { name: /^Back up/ })
    .click();
  expect(backup).toBeUndefined();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Approve back up now" })
    .click();
  await expect
    .poll(() => backup)
    .toMatchObject({ action: "backup", owner_approved: true });

  await expect(
    list
      .locator('[data-service-id="svc-hermes"]')
      .getByRole("link", { name: /^Stack it/ }),
  ).toHaveAttribute("href", /adopt=1/);
  const shot = process.env.NODE_CARDS_SHOTS;
  if (shot) {
    await page.getByTestId("apps-view-cards").click();
    await page
      .getByTestId("apps-cards")
      .locator('[data-service-id="svc-immich"]')
      .getByRole("button", { name: /^More actions/ })
      .click();
    await page.screenshot({ path: `${shot}/apps-actions-desktop.png` });
  }
});

// Owner direction 2026-10-10: M and L cards open Brand's detail sheet, which
// lists the vendor's links and the matching download only when the catalog
// carries them.
test("an app's details open the sheet with its vendor links and downloads", async ({
  context,
  baseURL,
}) => {
  await mockLoggedInContext(context, { allowMockAuth: true });
  await context.route("**/api/v1/features", (route) =>
    fulfillJson(route, { security: [], beta: [], ux: [] }),
  );
  await context.route("**/api/v1/services**", (route) =>
    fulfillJson(route, [
      service("svc-immich", "immich", "immich", "https://photos.home.example", {
        management_state: "managed",
        catalog: {
          id: "immich",
          display_name: "Immich",
          vendor: {
            name: "Immich",
            website: "https://immich.app",
            support: [{ kind: "purchase", url: "https://buy.immich.app" }],
          },
          clients: [
            {
              platform: "android",
              kind: "store",
              url: "https://play.google.com/store/apps/details?id=app.alextran.immich",
            },
          ],
        },
      }),
    ]),
  );
  const page = await context.newPage();
  await page.goto(`${baseURL}/apps`);
  await page.getByTestId("apps-view-list").click();
  await page.getByRole("button", { name: /^Details/ }).first().click();

  const sheet = page.getByRole("dialog");
  await expect(
    sheet.getByRole("link", { name: /Google Play/ }),
  ).toHaveAttribute("href", /play\.google\.com/);
  await expect(sheet.locator('a[href="https://buy.immich.app"]')).toBeVisible();
});
