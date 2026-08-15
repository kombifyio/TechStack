import { describe, expect, it } from "vitest";
import type { StackOperationServer } from "$lib/api/stacks";
import { isLegacyOrUnboundCustody } from "./server-custody";

function server(
  capabilities: StackOperationServer["capabilities"],
): Pick<StackOperationServer, "capabilities"> {
  return { capabilities };
}

describe("server custody classification", () => {
  it("enables stale-record resolution only for backend-classified legacy or unbound custody", () => {
    expect(
      isLegacyOrUnboundCustody(
        server({
          authority_state: "legacy_quarantined",
          execution_authority: "legacy_simulate",
        }),
      ),
    ).toBe(true);
    expect(
      isLegacyOrUnboundCustody(server({ authority_state: "unbound" })),
    ).toBe(true);
  });

  it("fails closed for provider-controlled, native-inactive, and unknown records", () => {
    expect(
      isLegacyOrUnboundCustody(
        server({
          authority_state: "native_active",
          execution_authority: "techstack_provider_control",
        }),
      ),
    ).toBe(false);
    expect(
      isLegacyOrUnboundCustody(
        server({
          authority_state: "native_inactive",
          execution_authority: "techstack_provider_control",
        }),
      ),
    ).toBe(false);
    expect(isLegacyOrUnboundCustody(server({}))).toBe(false);
  });
});
