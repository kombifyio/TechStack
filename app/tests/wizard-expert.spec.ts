import { expect, test } from "@playwright/test";
import { mockLoggedInContext, requireAppBase } from "./helpers/test-utils";

test("Easy Wizard and Techie Wizard are the two selectable creation surfaces", async ({
  browser,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);

  const context = await browser.newContext();
  await context.grantPermissions(["notifications"], { origin });
  await mockLoggedInContext(context, { allowMockAuth: true });
  const page = await context.newPage();

  await page.goto(`${origin}/stacks/new`);
  await page.getByTestId("hydrated").waitFor({ state: "attached" });

  await expect(page.getByTestId("tab-easy")).toBeEnabled();
  await expect(page.getByTestId("tab-techie")).toBeEnabled();
  await expect(page.getByTestId("easy-step-1")).toBeVisible();

  await page.getByTestId("tab-techie").click();
  await expect(page.getByTestId("easy-wizard")).toHaveCount(0);
  await expect(page.getByTestId("techie-step-1")).toBeVisible();

  await page.getByTestId("tab-easy").click();
  await expect(page.getByTestId("techie-wizard")).toHaveCount(0);
  await expect(page.getByTestId("easy-step-1")).toBeVisible();

  await context.close();
});
