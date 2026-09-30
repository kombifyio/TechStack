import { describe, expect, it } from "vitest";
import { getDefaultEnabled } from "./features";

describe("feature flag defaults", () => {
  it("keeps security features disabled before the backend loads", () => {
    expect(getDefaultEnabled("network_discovery")).toBe(false);
    expect(getDefaultEnabled("raw_commands")).toBe(false);
    expect(getDefaultEnabled("ssh_tunnel")).toBe(false);
    expect(getDefaultEnabled("cloud_backup")).toBe(false);
  });

  it("fails safe for unknown features", () => {
    expect(getDefaultEnabled("unknown_feature")).toBe(false);
    expect(getDefaultEnabled("")).toBe(false);
    expect(getDefaultEnabled("some_random_key")).toBe(false);
  });
});
