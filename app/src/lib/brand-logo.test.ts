import { describe, expect, it } from "vitest";
import { brandDomainForTool, logoLinkUrl } from "./brand-logo.js";

describe("brandDomainForTool", () => {
  it("maps StackKits application keys to vendor domains", () => {
    expect(brandDomainForTool("coolify")).toBe("coolify.io");
    expect(brandDomainForTool("id", "PocketID")).toBe("pocket-id.org");
    expect(brandDomainForTool("auth", "TinyAuth")).toBe("tinyauth.app");
    expect(brandDomainForTool("base", "Node Hub")).toBe("kombify.io");
  });

  it("leaves unknown tools without a domain", () => {
    expect(brandDomainForTool("custom-app", "My App")).toBe("");
  });
});

describe("logoLinkUrl", () => {
  it("builds a Logo Link URL only when both domain and client id are present", () => {
    expect(logoLinkUrl("coolify.io", "")).toBe("");
    expect(logoLinkUrl("", "brandLL_test")).toBe("");
    const url = logoLinkUrl("coolify.io", "brandLL_test", { theme: "dark" });
    expect(url.startsWith("https://logos.context.dev/?")).toBe(true);
    expect(url).toContain("domain=coolify.io");
    expect(url).toContain("publicClientId=brandLL_test");
    expect(url).toContain("theme=dark");
    expect(url).toContain("type=icon");
  });

  it("sends only normalized public domains to Logo Link", () => {
    const url = logoLinkUrl(" COOLIFY.IO. ", "brandLL_test");
    expect(url).toContain("domain=coolify.io");

    expect(logoLinkUrl("https://coolify.io", "brandLL_test")).toBe("");
    expect(logoLinkUrl("coolify.io:443", "brandLL_test")).toBe("");
    expect(logoLinkUrl("localhost", "brandLL_test")).toBe("");
    expect(logoLinkUrl("127.0.0.1", "brandLL_test")).toBe("");
    expect(logoLinkUrl("kombify.io", "brandLL_test")).toBe("");
  });
});
