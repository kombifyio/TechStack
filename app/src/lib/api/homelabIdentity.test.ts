import { describe, expect, it } from "vitest";
import { trustedIdentityActivationUrl } from "./homelabIdentity";

// Auth boundary: an enrollment bearer must only navigate to the verified RP.
describe("identity enrollment destination", () => {
  it("accepts a pending HTTPS link only on its verified identity origin", () => {
    const origin = "https://id.home.example";
    expect(
      trustedIdentityActivationUrl({
        status: "pending",
        origin,
        activation_url: `${origin}/setup?token=opaque`,
      }),
    ).toBe(`${origin}/setup?token=opaque`);
    for (const activation_url of [
      "https://other.example/setup?token=opaque",
      "http://id.home.example/setup",
      "javascript:alert(1)",
      "https://owner:secret@id.home.example/setup",
      "/setup?token=opaque",
    ])
      expect(
        trustedIdentityActivationUrl({
          status: "pending",
          origin,
          activation_url,
        }),
      ).toBeNull();
    expect(
      trustedIdentityActivationUrl({
        status: "active",
        origin,
        activation_url: `${origin}/setup`,
      }),
    ).toBeNull();
    expect(
      trustedIdentityActivationUrl({
        status: "expired",
        origin,
        activation_url: `${origin}/setup`,
      }),
    ).toBeNull();
  });
});
