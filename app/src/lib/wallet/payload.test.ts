import { describe, expect, it } from "vitest";
import { buildWalletEntryPayload } from "./payload";

describe("wallet payload builder", () => {
  it("marks tool entries as launch/open and revealable when secrets exist", () => {
    expect(
      buildWalletEntryPayload("tools", {
        name: "Admin",
        kind: "password",
        secret: "secret",
        stack_id: "stack-1",
      }),
    ).toMatchObject({
      item_class: "launch",
      access_mode: "open",
      revealable: true,
      source_type: "stack",
      source_ref: "stack-1",
    });
  });

  it("prefers explicit source metadata", () => {
    expect(
      buildWalletEntryPayload(
        "recovery",
        {
          name: "Key",
          kind: "ssh_key",
          service_id: "service-1",
        },
        { sourceType: "trust", sourceRef: "device-key" },
      ),
    ).toMatchObject({
      item_class: "recovery",
      access_mode: "reveal",
      revealable: false,
      source_type: "trust",
      source_ref: "device-key",
    });
  });
});
