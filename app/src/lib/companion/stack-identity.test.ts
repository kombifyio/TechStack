import { describe, expect, it } from "vitest";

import { companionStackIdentityFromAccount } from "./stack-identity";

describe("companionStackIdentityFromAccount", () => {
  it("maps account identity into launcher emoji and glow", () => {
    const mapped = companionStackIdentityFromAccount({
      name: "Nova Lab",
      characterId: "robot",
      animationStyle: "identity-glitch",
      animationEnabled: true,
      iconStyle: "filled",
      glowColorOverride: null,
      savedAt: null,
    });

    expect(mapped?.emoji).toBe("🤖");
    expect(mapped?.name).toBe("Nova Lab");
    expect(mapped?.animated).toBe(true);
    expect(mapped?.glowColor).toContain("oklch");
  });

  it("honours a glow override", () => {
    const mapped = companionStackIdentityFromAccount({
      name: "Glow",
      characterId: "rocket",
      animationStyle: "identity-float",
      animationEnabled: false,
      iconStyle: "glass",
      glowColorOverride: "oklch(0.7 0.2 40)",
      savedAt: null,
    });

    expect(mapped?.glowColor).toBe("oklch(0.7 0.2 40)");
    expect(mapped?.animated).toBe(false);
  });

  it("returns undefined without a character", () => {
    expect(companionStackIdentityFromAccount(null)).toBeUndefined();
  });
});
