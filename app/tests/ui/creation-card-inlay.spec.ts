import { test, expect } from "./fixtures";

test("advanced card inlay uses the full body and preserves edits when closed", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  for (const width of [1360, 390]) {
    await page.setViewportSize({ width, height: 950 });
    await page.goto("/preview/creation-wizard");
    const card = page.getByTestId("preview-goal-photos");
    await card
      .getByRole("button", { name: "Explore & customize", exact: true })
      .click();
    const advanced = card.getByRole("button", { name: /^Advanced/ });
    const service = card.getByRole("button", {
      name: "Immich Suggested",
      exact: true,
    });
    await expect(service).toBeVisible();
    await advanced.click();
    await expect(service).toBeHidden();
    await expect(
      card.getByRole("tab", { name: "Profile", exact: true }),
    ).toBeInViewport();
    const tabs = await card.getByRole("tab").all();
    const tabBounds = await Promise.all(tabs.map((tab) => tab.boundingBox()));
    for (const box of tabBounds) {
      expect(box!.y).toBeCloseTo(tabBounds[0]!.y, 0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(
        (await card.boundingBox())!.x + (await card.boundingBox())!.width,
      );
    }
    const panel = card.getByRole("tabpanel").filter({ visible: true });
    const bounds = await card.boundingBox();
    const panelBounds = await panel.boundingBox();
    expect(panelBounds!.width).toBeGreaterThan(bounds!.width * 0.85);
    const radio = panel.locator('input[type="radio"][value="high"]');
    await radio.check();
    await advanced.click();
    await expect(service).toBeVisible();
    await advanced.click();
    await expect(radio).toBeChecked();
    const currentBounds = await card.boundingBox();
    const currentPanelBounds = await panel.boundingBox();
    const toggleBounds = await advanced.boundingBox();
    expect(toggleBounds!.y).toBeGreaterThan(currentPanelBounds!.y);
    expect(toggleBounds!.y + toggleBounds!.height).toBeLessThanOrEqual(
      currentBounds!.y + currentBounds!.height,
    );
  }
});

test("document add-on is configured in Advanced and retained after closing", async ({
  page,
}) => {
  await page.goto("/preview/creation-wizard");
  const card = page.getByTestId("preview-goal-files");
  await card
    .getByRole("button", { name: "Explore & customize", exact: true })
    .click();
  const addon = card.getByRole("checkbox", { name: /Paperless/ });
  await expect(addon).toBeHidden();
  const advanced = card.getByRole("button", { name: /^Advanced/ });
  await advanced.click();
  await card.getByRole("tab", { name: /^Features/ }).click();
  await addon.check();
  await advanced.click();
  await expect(addon).toBeHidden();
  await advanced.click();
  await card.getByRole("tab", { name: /^Features/ }).click();
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
    .getByRole("heading", { name: "Choose your use cases", exact: true })
    .click();
  await expect(explore).toBeVisible();
  await explore.click();
  await expect(nextcloud).toHaveAttribute("aria-pressed", "true");
  await card.getByRole("button", { name: /^Advanced/ }).click();
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
