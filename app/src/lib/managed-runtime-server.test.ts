import { describe, expect, it } from "vitest";

import { isManagedRuntimeServer } from "./managed-runtime-server";

describe("isManagedRuntimeServer", () => {
  it("keeps a canonical Guard projection managed when it carries a lease", () => {
    expect(
      isManagedRuntimeServer({
        source: "canonical-server",
        lease_id: "lease-ionos-1",
      }),
    ).toBe(true);
  });

  it("does not classify a user-owned canonical server as managed", () => {
    expect(
      isManagedRuntimeServer({
        source: "canonical-server",
        lease_id: "",
      }),
    ).toBe(false);
  });
});
