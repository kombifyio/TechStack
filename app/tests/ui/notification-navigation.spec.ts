import { test, expect } from "./fixtures";

test("notification shortcuts let the owner read the inbox and change preferences", async ({
  page,
}) => {
  const subject = "Backup completed";
  const body = "The nightly backup is ready to restore.";
  const item = {
    id: "9afddadc-a49a-4b02-9666-715d42596917",
    subject,
    body_markdown: body,
    created_at: new Date().toISOString(),
    source_app: "techstack",
    topic_key: "lifecycle.completed",
  };
  let preferences = {
    topics: [
      {
        topic_key: "lifecycle.completed",
        category: "system",
        channels: { in_app: { enabled: true, locked: false } },
      },
    ],
  };
  await page.route("**/api/v2/whoami", (route) =>
    route.fulfill({
      json: {
        subject: "cloud-owner",
        tenantId: "owner-tenant",
        email: "owner@example.com",
        provider: "primary",
        role: "owner",
      },
    }),
  );
  await page.route("**/api/v1/notifications/**", async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    if (pathname.endsWith("/preferences")) {
      if (request.method() === "PUT") preferences = request.postDataJSON();
      await route.fulfill({ json: preferences });
    } else if (pathname.endsWith("/read")) {
      await route.fulfill({ json: {} });
    } else if (pathname.endsWith("/feed")) {
      await route.fulfill({
        json: {
          feed: [item],
          unread_count: 1,
          total_count: 1,
          has_more: false,
        },
      });
    } else {
      await route.fallback();
    }
  });
  await page.goto("/settings");
  await page
    .getByRole("button", { name: "Notifications", exact: true })
    .click();
  await page
    .getByRole("link", { name: /open (notification center|feed)/i })
    .click();
  await page.getByRole("button", { name: new RegExp(subject) }).click();
  await expect(
    page.getByRole("article").getByText(body, { exact: true }),
  ).toBeVisible();
  await page.waitForURL((url) => url.searchParams.get("item") === item.id);
  await page.reload();
  await expect(
    page.getByRole("article").getByText(body, { exact: true }),
  ).toBeVisible();

  await page
    .getByRole("button", { name: "Notifications", exact: true })
    .click();
  await page
    .getByRole("dialog", { name: /notification preview/i })
    .getByRole("link", { name: /(notification|feed( & push)?) settings/i })
    .click();
  const inApp = page.getByRole("button", { name: /in.app/i });
  await expect(inApp).toBeInViewport();
  await inApp.click();
  const saved = page.waitForResponse(
    (response) =>
      response.request().method() === "PUT" &&
      new URL(response.url()).pathname.endsWith("/preferences"),
  );
  await page.getByRole("button", { name: /save preferences/i }).click();
  await saved;
  expect(preferences.topics[0].channels.in_app.enabled).toBe(false);
});
