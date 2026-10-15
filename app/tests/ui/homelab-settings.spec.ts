import { type Page } from "@playwright/test";
import { test, expect } from "./fixtures";
import { mockLoggedInContext } from "../helpers/test-utils";

const identityOrigin = "https://id.basement.kombify.me";

async function mockHomelab(page: Page, ownerStatus: "pending" | "active") {
  await page.route("**/api/v1/homelab", async (route) => {
    await route.fulfill({
      json: {
        data: {
          homelab: {
            id: "hl-1",
            name: "My Homelab",
            intent: { goals: ["photos"] },
          },
          kit_deployments: [
            {
              id: "stack-1",
              kit_deployment_id: "stack-1",
              name: "basement",
              status: "running",
            },
          ],
        },
      },
    });
  });
  let owner = ownerStatus;
  await page.route("**/api/v1/stacks/stack-1/identity", async (route) => {
    await route.fulfill({
      json: {
        data: {
          owner: { status: owner, origin: identityOrigin },
          household: {
            planned_people: [
              { client_ref: "p-1", name: "Alex", email: "alex@example.com" },
            ],
            members: [],
          },
        },
      },
    });
  });
  await page.route(
    "**/api/v1/stacks/stack-1/identity/owner-activation",
    async (route) => {
      owner = "active";
      await route.fulfill({
        json: { data: { owner: { status: "active", origin: identityOrigin } } },
      });
    },
  );
}

test.describe("Homelab settings wizard", () => {
  test.beforeEach(async ({ page }) => {
    await mockLoggedInContext(page.context(), { allowMockAuth: true });
  });

  test("edits only Users and Login, through the identity runtime", async ({
    page,
  }) => {
    let invitation: Record<string, unknown> | undefined;
    await mockHomelab(page, "active");
    await page.route(
      "**/api/v1/stacks/stack-1/identity/household-invitations",
      async (route) => {
        invitation = route.request().postDataJSON();
        await route.fulfill({
          json: {
            data: {
              invitation: {
                status: "pending",
                origin: identityOrigin,
                activation_url: `${identityOrigin}/invite/abc`,
              },
            },
          },
        });
      },
    );

    await page.goto("/settings");
    await page.getByTestId("open-homelab-settings").click();
    await expect(page.getByTestId("homelab-settings-page")).toBeVisible();

    for (const id of [1, 2, 3]) {
      await expect(page.getByTestId(`wizard-step-${id}`)).toHaveAttribute(
        "data-state",
        "inactive",
      );
    }
    await expect(page.getByTestId("homelab-settings-users")).toBeVisible();
    await page.getByRole("button", { name: "Prepare access" }).click();
    await expect
      .poll(() => invitation)
      .toMatchObject({ email: "alex@example.com", owner_approved: true });
    await expect(page.getByRole("button", { name: "Copy personal link" })).toBeVisible();

    await page.getByTestId("wizard-next").click();
    await expect(page.getByTestId("homelab-settings-login")).toContainText(
      identityOrigin,
    );
    await expect(page.getByTestId("homelab-settings-users")).toBeHidden();
  });

  test("activates the owner passkey from the Login module", async ({
    page,
  }) => {
    let activated = false;
    await mockHomelab(page, "pending");
    page.on("request", (request) => {
      if (request.url().includes("/identity/owner-activation"))
        activated = true;
    });

    await page.goto("/settings/homelab");
    await page.getByTestId("wizard-next").click();
    const login = page.getByTestId("homelab-settings-login");
    await login.getByRole("button", { name: "Set up my passkey" }).click();
    await expect.poll(() => activated).toBe(true);
  });
});
