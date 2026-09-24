import { describe, expect, it } from "vitest";

import {
  companionMountOptions,
  resolveCompanionAppearance,
  resolveCompanionFinish,
  type CompanionHostContext,
} from "./options";

const MESSAGES = {
  frameTitle: "kombify Companion",
  openLauncher: "Open the Companion",
  closeLauncher: "Close the Companion",
  startVoiceAgent: "Start voice agent",
  launcherLabel: "Ask kombify",
};

const TOKEN = () => Promise.resolve("user-gateway-token");

function context(
  overrides: Partial<CompanionHostContext> = {},
): CompanionHostContext {
  return {
    getToken: TOKEN,
    appearance: "dark",
    finish: "aurora",
    locale: "de",
    messages: MESSAGES,
    ...overrides,
  };
}

describe("companionMountOptions", () => {
  it("takes the shared launcher rather than building a third mascot", () => {
    // The whole reason this host is ~100 lines: the SDK's built-in launcher IS
    // the Workbench Kubi. Flipping this to "none" means owning one here.
    expect(companionMountOptions(context()).launcher).toBe("builtin");
  });

  it("carries the user's own Gateway token, not a service credential", () => {
    // Not interchangeable with host-relay: the Gateway edge accepts service
    // auth in place of a user JWT on four paths, none of them the Companion's,
    // so a service-signed relay gets 401 from production. `/v1/ai/*` entitles
    // against the real user, which is exactly why.
    const options = companionMountOptions(context());

    expect(options.transport).toEqual({ mode: "token", getToken: TOKEN });
  });

  it("hands over the minting function rather than a minted token", async () => {
    // Per request, not once: a token refreshed mid-conversation has to reach
    // the panel without the iframe being replaced.
    const transport = companionMountOptions(context()).transport;

    expect(transport.mode).toBe("token");
    if (transport.mode !== "token") throw new Error("unreachable");
    await expect(transport.getToken()).resolves.toBe("user-gateway-token");
  });

  it("passes the host's resolved appearance, finish and locale through", () => {
    // Without these the panel cannot match the app it is embedded in: it can
    // read neither the document's theme nor its language from inside an iframe.
    const options = companionMountOptions(context());

    expect(options.theme).toBe("dark");
    expect(options.design).toEqual({ finish: "aurora" });
    expect(options.locale).toBe("de");
  });

  it("carries the host's own launcher copy", () => {
    expect(companionMountOptions(context()).messages).toEqual(MESSAGES);
  });

  it("identifies Techstack assistant traffic to the central model policy", () => {
    expect(companionMountOptions(context()).aiWorkload).toBe("assistant");
  });

  it("forwards stack identity into the embed launcher", () => {
    const stackIdentity = {
      emoji: "🤖",
      glowColor: "oklch(0.75 0.2 145)",
      animated: true,
      name: "Nova",
    };
    expect(companionMountOptions(context({ stackIdentity })).stackIdentity).toEqual(
      stackIdentity,
    );
  });
});

describe("resolveCompanionAppearance", () => {
  it("maps the document's resolved theme, never the raw preference", () => {
    expect(resolveCompanionAppearance("light")).toBe("light");
    expect(resolveCompanionAppearance("dark")).toBe("dark");
  });

  it("falls back to dark when the document says nothing", () => {
    expect(resolveCompanionAppearance(undefined)).toBe("dark");
    expect(resolveCompanionAppearance("system")).toBe("dark");
  });
});

describe("resolveCompanionFinish", () => {
  it("accepts every finish on the axis", () => {
    for (const finish of [
      "kombify",
      "liquid",
      "frost",
      "expressive",
      "aurora",
    ]) {
      expect(resolveCompanionFinish(finish)).toBe(finish);
    }
  });

  it("falls back to the baseline for anything outside the closed set", () => {
    // The value comes off a DOM attribute, so it is attacker-shaped input in
    // the sense that matters: unvalidated, it would be forwarded to the panel.
    expect(resolveCompanionFinish("neon-pink")).toBe("kombify");
    expect(resolveCompanionFinish(undefined)).toBe("kombify");
    expect(resolveCompanionFinish("")).toBe("kombify");
  });
});
