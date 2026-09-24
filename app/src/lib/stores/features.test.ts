import { describe, expect, it } from "vitest";
import { getDefaultEnabled } from "./features";

describe("feature flag defaults", () => {
  it("keeps security features disabled before the backend loads", () => {
    expect(getDefaultEnabled("network_discovery")).toBe(false);
    expect(getDefaultEnabled("raw_commands")).toBe(false);
    expect(getDefaultEnabled("ssh_tunnel")).toBe(false);
    expect(getDefaultEnabled("cloud_backup")).toBe(false);
  });

  it("applies the explicit beta defaults", () => {
    expect(getDefaultEnabled("native_v2_wizard")).toBe(true);
    expect(getDefaultEnabled("cloudflare_tunnel")).toBe(false);
    expect(getDefaultEnabled("self_healing")).toBe(false);
    expect(getDefaultEnabled("ha_stackkit")).toBe(false);
  });

  it("keeps UX features enabled before the backend loads", () => {
    expect(getDefaultEnabled("onboarding_wizard")).toBe(true);
    expect(getDefaultEnabled("keyboard_shortcuts")).toBe(true);
    expect(getDefaultEnabled("dark_mode")).toBe(true);
    expect(getDefaultEnabled("use_case_photos")).toBe(true);
    expect(getDefaultEnabled("use_case_mail")).toBe(true);
  });

  it("fails safe for unknown features", () => {
    expect(getDefaultEnabled("unknown_feature")).toBe(false);
    expect(getDefaultEnabled("")).toBe(false);
    expect(getDefaultEnabled("some_random_key")).toBe(false);
  });
});
