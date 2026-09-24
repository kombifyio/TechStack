import { test, expect } from "@playwright/test";
import { mockLoggedInContext } from "./helpers/test-utils";

test("explicit hypervisor choice mints only a substrate capability", async ({
  page,
}) => {
  await mockLoggedInContext(page.context(), { allowMockAuth: true });
  await page.route("**/api/v1/stacks/homelab-fixture", (route) =>
    route.fulfill({
      json: {
        data: {
          id: "homelab-fixture",
          name: "Home",
          stackkit_catalog_ref: "basement-kit",
        },
      },
    }),
  );
  await page.route("**/api/v1/trust/pairing-tokens", (route) => {
    expect(route.request().postDataJSON()).toMatchObject({
      node_role: "substrate",
      environment_class: "local",
      services: [],
    });
    expect(route.request().postDataJSON().stackkit).toBeUndefined();
    return route.fulfill({
      json: {
        data: {
          token: "kpt1.hypervisor-fixture",
          expires_at: new Date(Date.now() + 900000).toISOString(),
        },
      },
    });
  });
  await page.goto("/stacks/homelab-fixture/servers/new");
  await expect(
    page.getByRole("button", { name: "New StackKit / main Node", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Proxmox hypervisor", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Generate connection command", exact: true })
    .click();
  await expect(
    page.getByTestId("substrate-registration-command"),
  ).toContainText("kpt1.hypervisor-fixture");
  const persisted = await page.evaluate(() =>
    JSON.stringify({
      session: { ...sessionStorage },
      local: { ...localStorage },
      state: history.state,
    }),
  );
  expect(persisted).not.toContain("kpt1.hypervisor-fixture");
});

test("recovers a redacted pairing command without persisting its credential", async ({
  page,
}) => {
  const token = "kpt1.local-fixture-only";
  await mockLoggedInContext(page.context(), { allowMockAuth: true });
  await page.route("**/api/v1/jobs/pairing-fixture", (route) =>
    route.fulfill({
      json: {
        data: {
          id: "pairing-fixture",
          type: "update",
          state: "completed",
          progress: 100,
          result: {
            creation_operation: "add-server",
            stack_id: "homelab-fixture",
            server_provisioning_mode: "install-command",
            server_node_role: "worker",
            stackkit_foundation: "basement-kit",
            requested_services: [],
          },
        },
      },
    }),
  );
  await page.route("**/api/v1/trust/pairing-tokens", async (route) => {
    expect(route.request().method()).toBe("POST");
    expect(route.request().postDataJSON()).toMatchObject({
      stack_id: "homelab-fixture",
      node_role: "worker",
      services: [],
    });
    await route.fulfill({
      json: {
        data: {
          token,
          expires_at: new Date(Date.now() + 900_000).toISOString(),
        },
      },
    });
  });
  await page.goto(
    "/stacks/creating?operation=add-server&stack_id=homelab-fixture&job_id=pairing-fixture&pairing_job_id=pairing-fixture",
  );
  await page
    .getByRole("button", { name: "Generate pairing command", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Copy pairing command", exact: true }),
  ).toBeVisible();
  await expect(page.getByTestId("server-registration-command")).toContainText(
    token,
  );
  await expect(
    page.getByText("Not connected yet", { exact: true }),
  ).toBeVisible();
  const persisted = await page.evaluate(() =>
    JSON.stringify({
      url: location.href,
      session: { ...sessionStorage },
      local: { ...localStorage },
      state: history.state,
    }),
  );
  expect(persisted).not.toContain(token);
  await page.reload();
  await expect(
    page.getByRole("button", { name: "Generate pairing command", exact: true }),
  ).toBeVisible();
});
