import { describe, expect, it } from "vitest";
import { resolveSettingsTab, settingsTabHref } from "./tabs.js";

describe("settings tab deep links", () => {
  it("round-trips every canonical href and falls back to Account for unknown slugs", () => {
    for (const slug of ["account", "identity", "design", "notifications", "homelab"] as const) {
      const href = settingsTabHref(slug);
      expect(resolveSettingsTab(new URL(href, "https://x.test").searchParams.get("tab"))).toBe(slug);
    }
    expect(resolveSettingsTab("Retired-Tab")).toBe("account");
    expect(resolveSettingsTab(null)).toBe("account");
  });
});
