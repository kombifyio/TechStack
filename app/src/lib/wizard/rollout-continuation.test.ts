import { describe, expect, it } from "vitest";
import {
  belongsToCurrentConnectionAttempt,
  requiresGuardConnection,
} from "./rollout-continuation";

describe("StackKit rollout continuation", () => {
  it("waits for a Guard connection for both initial and additional user-owned servers", () => {
    expect(requiresGuardConnection("stack", "install-command")).toBe(true);
    expect(requiresGuardConnection("stack", "connect-remote")).toBe(true);
    expect(requiresGuardConnection("add-server", "install-command")).toBe(
      true,
    );
    expect(requiresGuardConnection("stack", "kombify-cloud")).toBe(false);
  });

  it("binds initial creation to the exact stack when no prior-server baseline exists", () => {
    expect(
      belongsToCurrentConnectionAttempt({
        serverId: "server-new",
        serverStackId: "stack-new",
        stackId: "stack-new",
        existingServerBaselineKnown: false,
        existingServerIds: new Set(),
      }),
    ).toBe(true);
    expect(
      belongsToCurrentConnectionAttempt({
        serverId: "server-foreign",
        serverStackId: "stack-other",
        stackId: "stack-new",
        existingServerBaselineKnown: false,
        existingServerIds: new Set(),
      }),
    ).toBe(false);
  });

  it("keeps add-server matching bound to a newly observed server", () => {
    expect(
      belongsToCurrentConnectionAttempt({
        serverId: "server-old",
        serverStackId: "stack-new",
        stackId: "stack-new",
        existingServerBaselineKnown: true,
        existingServerIds: new Set(["server-old"]),
      }),
    ).toBe(false);
  });
});
