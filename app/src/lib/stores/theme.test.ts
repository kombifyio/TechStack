// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { get } from "svelte/store";

// The default mock is `browser: false`, which short-circuits getInitialTheme.
vi.mock("$app/environment", () => ({
  browser: true,
}));

const STORAGE_KEY = "techstack-theme";
const CARD_SHAPE_STORAGE_KEY = "techstack-system-card-shape";

describe("theme store", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.resetModules();
    document.documentElement.classList.remove("dark", "light");
    delete document.documentElement.dataset.systemCardShape;
    // Clear any ?theme= left by an embed test — getInitialTheme reads it.
    window.history.replaceState({}, "", "/");
    // jsdom ships no matchMedia; init() consults it for the "system" theme.
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({
        matches: false,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      })),
    );
  });

  it("defaults to dark when nothing else says otherwise", async () => {
    const { theme } = await import("./theme");
    theme.init();
    expect(get(theme)).toBe("dark");
  });

  it("restores an explicit local choice when running standalone", async () => {
    localStorage.setItem(STORAGE_KEY, "light");
    const { theme } = await import("./theme");
    theme.init();
    expect(get(theme)).toBe("light");
  });

  it("adopts the portal theme from ?theme= so the embed does not flash", async () => {
    // Kombify Cloud puts its own theme on the iframe src. Its `theme`
    // postMessage can only land after our bridge announces "ready", which is
    // too late for the first paint.
    window.history.replaceState({}, "", "/?embedded=true&theme=light");

    const { theme } = await import("./theme");
    theme.init();
    expect(get(theme)).toBe("light");
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("lets the portal theme win over a stored value while embedded", async () => {
    // An embed that does not match the page around it is the bug being fixed,
    // so the host wins when it states a theme.
    window.history.replaceState({}, "", "/?embedded=true&theme=light");
    localStorage.setItem(STORAGE_KEY, "dark");

    const { theme } = await import("./theme");
    theme.init();
    expect(get(theme)).toBe("light");
  });

  it("ignores a malformed theme param", async () => {
    window.history.replaceState({}, "", "/?theme=chartreuse");
    localStorage.setItem(STORAGE_KEY, "light");

    const { theme } = await import("./theme");
    theme.init();
    expect(get(theme)).toBe("light");
  });

  it("defaults canonical service and server cards to square", async () => {
    const { theme } = await import("./theme");
    theme.init();
    expect(document.documentElement.dataset.systemCardShape).toBe("square");
  });

  it("adopts the portal card shape before the first embedded paint", async () => {
    window.history.replaceState(
      {},
      "",
      "/?embedded=true&system_card_shape=app",
    );
    localStorage.setItem(CARD_SHAPE_STORAGE_KEY, "square");

    const { theme } = await import("./theme");
    theme.init();
    expect(document.documentElement.dataset.systemCardShape).toBe("app");
  });

  it("persists a live card-shape update on the existing appearance store", async () => {
    const { theme } = await import("./theme");
    theme.setSystemCardShape("app");
    expect(localStorage.getItem(CARD_SHAPE_STORAGE_KEY)).toBe("app");
    expect(document.documentElement.dataset.systemCardShape).toBe("app");
  });
});
