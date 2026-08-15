import { describe, expect, it } from "vitest";
import { cleanupActionForFailure } from "./cleanup-action";

const leases = [
  { lease_id: "lease-native", allowed_actions: ["decommission"] },
  { lease_id: "lease-legacy", allowed_actions: ["resolve_custody"] },
] as const;

describe("failed cleanup action selection", () => {
  it("keeps retry bound to the exact provider-managed lease", () => {
    expect(cleanupActionForFailure(leases, "lease-native")).toEqual({
      lease: leases[0],
      action: "decommission",
    });
  });

  it("routes an exact legacy failure to manual custody resolution", () => {
    expect(cleanupActionForFailure(leases, "lease-legacy")).toEqual({
      lease: leases[1],
      action: "resolve_custody",
    });
  });

  it("never borrows an unrelated lease when the failed identity is absent", () => {
    expect(cleanupActionForFailure(leases, "lease-missing")).toBeNull();
    expect(cleanupActionForFailure(leases, undefined)).toBeNull();
  });
});
