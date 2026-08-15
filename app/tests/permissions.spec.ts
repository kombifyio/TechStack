import { test, expect } from "@playwright/test";
import { requireAppBase } from "./helpers/test-utils";

test("grant notifications permission to avoid manual allow click", async ({
  browser,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  const context = await browser.newContext();
  await context.grantPermissions(["notifications"], { origin });

  const page = await context.newPage();
  await page.goto(`${origin}/stacks/new`);

  // Request permission in page context; with granted permission Playwright should auto-approve
  const result = await page.evaluate(() =>
    typeof Notification !== "undefined"
      ? Notification.requestPermission()
      : "unsupported",
  );

  expect(result).toBe("granted");
});
