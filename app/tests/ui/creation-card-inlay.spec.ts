import { test, expect } from "./fixtures";

test("guided setup applies the recommendation and adds the use case", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  for (const width of [1360, 390]) {
    await page.setViewportSize({ width, height: 950 });
    await page.goto("/preview/creation-wizard");
    const card = page.getByTestId("preview-goal-photos");
    const explore = card.getByRole("button", {
      name: "Explore & customize",
      exact: true,
    });
    await explore.click();
    await card
      .getByRole("button", { name: "Use our recommendation", exact: true })
      .click();
    await card
      .getByRole("button", { name: "Apply setup", exact: true })
      .click();
    await expect(explore).toBeVisible();
    await expect(
      card.getByRole("button", { name: /^Remove from your kit/ }),
    ).toBeVisible();
  }
});

test("setup answers survive closing the card", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/preview/creation-wizard");
  const card = page.getByTestId("preview-goal-files");
  const explore = card.getByRole("button", {
    name: "Explore & customize",
    exact: true,
  });
  await explore.click();
  await card.getByRole("button", { name: /^Extras/ }).click();
  const addon = card.getByRole("checkbox", { name: /^Document archive/ });
  await card.locator("label").filter({ hasText: "Document archive" }).click();
  await expect(addon).toBeChecked();
  await page
    .getByRole("button", { name: "Less detail Documents & Files", exact: true })
    .click();
  await explore.click();
  await card.getByRole("button", { name: /^Extras/ }).click();
  await expect(addon).toBeChecked();
});

test("expanded cards dismiss without losing the selected application", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await page.setViewportSize({ width: 1360, height: 950 });
  await page.goto("/preview/creation-wizard");
  const card = page.getByTestId("preview-goal-files");
  const explore = card.getByRole("button", {
    name: "Explore & customize",
    exact: true,
  });
  const nextcloud = card.getByRole("button", {
    name: /^Nextcloud Alternative/,
  });
  await explore.click();
  await nextcloud.click();
  await page
    .getByRole("heading", { name: "Make room for what matters.", exact: true })
    .click();
  await expect(explore).toBeVisible();
  await explore.click();
  await expect(nextcloud).toHaveAttribute("aria-pressed", "true");
  await card.getByRole("button", { name: /^Extras/ }).click();
  await page.keyboard.press("Escape");
  await expect(explore).toBeFocused();
  await explore.click();
  await page
    .getByRole("button", { name: "Less detail Documents & Files", exact: true })
    .click();
  await expect(explore).toBeFocused();
  await explore.click();
  await expect(nextcloud).toHaveAttribute("aria-pressed", "true");
});

test("steps above the detail level open under More settings", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1360, height: 950 });
  await page.goto("/preview/creation-wizard");
  const card = page.getByTestId("preview-goal-photos");
  await card
    .getByRole("button", { name: "Explore & customize", exact: true })
    .click();
  const steps = card.getByRole("list", { name: "Setup steps" }).locator("li");
  const guided = await steps.count();
  await card.getByRole("button", { name: /^More settings/ }).click();
  await expect(steps).not.toHaveCount(guided);
  expect(await steps.count()).toBeGreaterThan(guided);
  await card
    .getByRole("button", { name: "Fewer settings", exact: true })
    .click();
  await expect(steps).toHaveCount(guided);
});
