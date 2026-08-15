import { describe, expect, it } from "vitest";
import {
  cloudDeviceLoginUrl,
  cloudLoginUrl,
  cloudUiLoginUrl,
  deriveLocalOwnerName,
  hasWindowsLocalClientContext,
  loginRedirectForWindowsLocalClient,
  localSetupReady,
  normalizeServerUrl,
  rememberWindowsLocalClientContext,
  windowsLocalClientReturnUrl,
  windowsClientModeLabel,
} from "./windows-onboarding";

describe("windows client onboarding helpers", () => {
  it("routes cloud login through hosted kombify Cloud sign in", () => {
    expect(cloudLoginUrl).toBe(cloudDeviceLoginUrl);
    expect(cloudDeviceLoginUrl).toBe("https://kombify.io/device");
    expect(cloudUiLoginUrl).toBe(
      "https://techstack.kombify.io/login?manual=1&client=windows",
    );
    expect(cloudLoginUrl).not.toContain("callbackUrl=%2Fdashboard");
    expect(cloudLoginUrl).not.toContain("source=techstack-desktop");
  });

  it("normalizes self-hosted server URLs", () => {
    expect(normalizeServerUrl("home.example.test/")).toBe(
      "https://home.example.test",
    );
    expect(normalizeServerUrl("http://127.0.0.1:5261/")).toBe(
      "http://127.0.0.1:5261",
    );
  });

  it("keeps local install as default label", () => {
    expect(windowsClientModeLabel(null)).toBe("Local Windows installation");
  });

  it("derives a deterministic local owner name from the email", () => {
    expect(deriveLocalOwnerName(" owner@example.com ")).toBe("owner");
    expect(deriveLocalOwnerName("owner")).toBe("owner");
  });

  it("requires a usable email and an eight-character password for local setup", () => {
    expect(localSetupReady("owner@example.com", "password1")).toBe(true);
    expect(localSetupReady("", "password1")).toBe(false);
    expect(localSetupReady("owner@example.com", "short")).toBe(false);
  });

  it("marks the local Windows client context for logout return routing", () => {
    const data = new Map<string, string>();
    const storage = {
      get length() {
        return data.size;
      },
      clear: () => data.clear(),
      getItem: (key: string) => data.get(key) ?? null,
      key: (index: number) => Array.from(data.keys())[index] ?? null,
      removeItem: (key: string) => data.delete(key),
      setItem: (key: string, value: string) => {
        data.set(key, value);
      },
    } as Storage;

    expect(windowsLocalClientReturnUrl).toBe("/client/local?client=windows");
    expect(hasWindowsLocalClientContext(storage)).toBe(false);

    rememberWindowsLocalClientContext(storage);

    expect(hasWindowsLocalClientContext(storage)).toBe(true);
    expect(loginRedirectForWindowsLocalClient(storage)).toBe(
      windowsLocalClientReturnUrl,
    );
  });
});
