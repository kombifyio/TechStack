// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { get } from "svelte/store";

vi.mock("$app/env", () => ({
  browser: true,
}));

describe("deployment mode navigation ownership", () => {
  beforeEach(() => {
    vi.resetModules();
    localStorage.clear();
    window.history.replaceState({}, "", "/");
  });

  it("hides the tool tab bar only for an explicit SaaS host-navigation embed", async () => {
    window.history.replaceState(
      {},
      "",
      "/dashboard?embedded=true&host_navigation=true",
    );

    const {
      deploymentMode,
      isHostNavigationOwned,
      showInlineTabs,
      showSidebar,
    } = await import("./deploymentMode");
    await deploymentMode.init("saas");

    expect(get(isHostNavigationOwned)).toBe(true);
    expect(get(showInlineTabs)).toBe(false);
    expect(get(showSidebar)).toBe(false);
  });

  it("keeps inline tabs for embedded SaaS without host ownership", async () => {
    window.history.replaceState({}, "", "/dashboard?embedded=true");

    const { deploymentMode, isHostNavigationOwned, showInlineTabs } =
      await import("./deploymentMode");
    await deploymentMode.init("saas");

    expect(get(isHostNavigationOwned)).toBe(false);
    expect(get(showInlineTabs)).toBe(true);
  });

  it("ignores host ownership on a self-hosted backend", async () => {
    window.history.replaceState(
      {},
      "",
      "/dashboard?embedded=true&host_navigation=true",
    );

    const { deploymentMode, isHostNavigationOwned, showSidebar } =
      await import("./deploymentMode");
    await deploymentMode.init("self-hosted");

    expect(get(isHostNavigationOwned)).toBe(false);
    expect(get(showSidebar)).toBe(true);
  });
});
