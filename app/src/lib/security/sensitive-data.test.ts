import { describe, expect, it } from "vitest";
import { sanitizeSensitiveText, scrubSensitiveValue } from "./sensitive-data";

describe("sensitive data scrubbing", () => {
  it("removes reusable Kombify credentials from persistent evidence", () => {
    const signature = "x".repeat(43);
    const captured = scrubSensitiveValue({
      registration_token: `kpt1.owner.${signature}`,
      log: `agent=tsra.opaque.${signature} legacy=ks_${"a".repeat(64)}`,
      nested: { a: { b: { c: { d: { e: { f: { secret: signature } } } } } } },
    });

    expect(JSON.stringify(captured)).not.toMatch(
      new RegExp(`kpt1\\.|tsra\\.|ks_[a-f0-9]|${signature}`, "i"),
    );
  });

  it("retains reason codes while bounding summaries", () => {
    expect(sanitizeSensitiveText("reason=server_offline")).toContain(
      "server_offline",
    );
    expect(sanitizeSensitiveText("x".repeat(3_000))).toHaveLength(2_000);
  });
});
