import { describe, expect, it } from "vitest";
import {
  brandDomainForTool,
  distinctToolLogos,
  logoLinkUrl,
} from "./brand-logo.js";

describe("brandDomainForTool", () => {
  it("maps StackKits application keys to vendor domains", () => {
    expect(brandDomainForTool("coolify")).toBe("coolify.io");
    expect(brandDomainForTool("id", "PocketID")).toBe("pocket-id.org");
    expect(brandDomainForTool("auth", "TinyAuth")).toBe("tinyauth.app");
    expect(brandDomainForTool("base", "Node Hub")).toBe("kombify.io");
  });

  it("resolves runtime unit, compose and image names to their product", () => {
    expect(brandDomainForTool("hermes-gateway.service")).toBe(
      "nousresearch.com",
    );
    expect(brandDomainForTool("system", "hermes-webui")).toBe(
      "nousresearch.com",
    );
    expect(brandDomainForTool("kombify-local-portal")).toBe("kombify.io");
    expect(brandDomainForTool("photos-immich-server-1")).toBe("immich.app");
    expect(
      brandDomainForTool("app", "ghcr.io/immich-app/immich-server:v1.120"),
    ).toBe("immich.app");
    // A generic alias never matches as a leftover fragment.
    expect(brandDomainForTool("home-server")).toBe("");
  });

  it("leaves unknown tools without a domain", () => {
    expect(brandDomainForTool("custom-app", "My App")).toBe("");
  });
});

describe("distinctToolLogos", () => {
  it("collapses shared application marks while preserving other and unknown tools", () => {
    const immich = { id: "immich-lite", name: "Immich Lite" };
    const nextcloud = { id: "nextcloud", name: "Nextcloud" };
    const customDrive = { id: "custom-drive", name: "Custom drive" };
    const customArchive = { id: "custom-archive", name: "Custom archive" };
    const tools = [
      { id: "immich-kiosk", name: "Immich Kiosk" },
      immich,
      nextcloud,
      { id: "immich-power-tools", name: "Immich Power Tools" },
      customDrive,
      { id: "immich-public-proxy", name: "Immich Public Proxy" },
      customArchive,
    ];

    expect(distinctToolLogos(tools, immich.id)).toEqual([
      immich,
      nextcloud,
      customDrive,
      customArchive,
    ]);
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
