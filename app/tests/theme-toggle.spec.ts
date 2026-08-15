import { test, expect } from "@playwright/test";

const STORAGE_KEY = "techstack-theme";

test.describe("Theme toggle", () => {
  test("should switch between dark and light mode on login", async ({
    page,
  }) => {
    await page.addInitScript(() => {
      window.localStorage.setItem("techstack-theme", "dark");
    });

    await page.goto("/login", { waitUntil: "domcontentloaded" });

    await expect(
      page.getByRole("button", { name: "Toggle dark/light theme" }),
    ).toBeVisible();

    const initialDark = await page.evaluate(() =>
      document.documentElement.classList.contains("dark"),
    );
    expect(initialDark).toBe(true);

    await page.getByRole("button", { name: "Toggle dark/light theme" }).click();

    const lightMode = await page.evaluate(() => {
      return {
        isDark: document.documentElement.classList.contains("dark"),
        dataTheme: document.documentElement.dataset.theme,
        stored: window.localStorage.getItem("techstack-theme"),
      };
    });

    expect(lightMode.isDark).toBe(false);
    expect(lightMode.dataTheme).toBe("light");
    expect(lightMode.stored).toBe("light");
  });
});
