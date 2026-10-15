// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { get } from "svelte/store";

// The default mock is `browser: false`, which short-circuits getInitialTheme.
vi.mock("$app/env", () => ({
  browser: true,
}));

const LEGACY_STORAGE_KEY = "techstack-theme";
const SESSION_KEY = "techstack-theme-session";

/** jsdom runs top-level; a different parent window makes this an embed. */
function embedInPortal() {
  Object.defineProperty(window, "parent", {
    configurable: true,
    value: {},
  });
}
const CARD_SHAPE_STORAGE_KEY = "techstack-system-card-shape";

describe("theme store", () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
    Object.defineProperty(window, "parent", {
      configurable: true,
      value: window,
    });
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

  it("follows the system appearance without saving an explicit choice", async () => {
    const { theme } = await import("./theme");
    theme.init();
    expect(get(theme)).toBe("system");
    expect(document.documentElement.dataset.appearance).toBe("light");
    expect(sessionStorage.getItem(SESSION_KEY)).toBeNull();
  });

  it("keeps a standalone switch for this session only and drops the retired device choice", async () => {
    localStorage.setItem(LEGACY_STORAGE_KEY, "light");
    const { theme } = await import("./theme");
    theme.init();
    expect(get(theme)).toBe("system");
    expect(localStorage.getItem(LEGACY_STORAGE_KEY)).toBeNull();
    theme.set("dark");
    expect(document.documentElement.dataset.appearance).toBe("dark");
    expect(sessionStorage.getItem(SESSION_KEY)).toBe("dark");
    expect(localStorage.getItem(LEGACY_STORAGE_KEY)).toBeNull();
  });

  it("adopts the portal theme from ?theme= so the embed does not flash", async () => {
    // kombify Cloud puts its own theme on the iframe src. Its `theme`
    // postMessage can only land after our bridge announces "ready", which is
    // too late for the first paint.
    embedInPortal();
    window.history.replaceState({}, "", "/?embedded=true&theme=light");

    const { theme } = await import("./theme");
    theme.init();
    expect(get(theme)).toBe("light");
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("lets the embedding host decide over any choice made inside the app", async () => {
    embedInPortal();
    window.history.replaceState({}, "", "/?embedded=true&theme=dark");
    sessionStorage.setItem(SESSION_KEY, "light");

    const { theme } = await import("./theme");
    expect(get(theme.resolved)).toBe("dark");
    theme.init();
    expect(document.documentElement.dataset.appearance).toBe("dark");
    theme.set("light");
    expect(document.documentElement.dataset.appearance).toBe("dark");
    theme.setHostTheme("light");
    expect(document.documentElement.dataset.appearance).toBe("light");
  });

  it("ignores a malformed theme param", async () => {
    embedInPortal();
    window.history.replaceState({}, "", "/?theme=chartreuse");
    sessionStorage.setItem(SESSION_KEY, "light");

    const { theme } = await import("./theme");
    theme.init();
    expect(get(theme)).toBe("light");
  });

  it("retains the host appearance when SSO and client navigation remove the launch query", async () => {
    embedInPortal();
    window.history.replaceState({}, "", "/auth/sso?embedded=true&theme=dark");
    sessionStorage.setItem(SESSION_KEY, "light");
    localStorage.setItem("techstack-account-theme", "light");

    const { theme } = await import("./theme");
    theme.init();
    expect(document.documentElement.dataset.appearance).toBe("dark");

    window.history.replaceState({}, "", "/auth/sso?embedded=true");
    theme.init();
    expect(document.documentElement.dataset.appearance).toBe("dark");

    window.history.replaceState({}, "", "/stacks/new?host_navigation=true");
    theme.set("light");
    expect(document.documentElement.dataset.appearance).toBe("dark");

    theme.setHostTheme("light");
    theme.init();
    expect(document.documentElement.dataset.appearance).toBe("light");
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
