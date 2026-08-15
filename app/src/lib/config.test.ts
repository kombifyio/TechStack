import { describe, expect, it } from "vitest";
import { appDeployLabel, appVersion, productIdentityLabel } from "./config";

describe("config", () => {
  describe("appVersion", () => {
    it("is a non-empty release identifier", () => {
      expect(appVersion.trim().length).toBeGreaterThan(0);
    });
  });

  describe("productIdentityLabel", () => {
    it("renders one shared version and short-revision label", () => {
      expect(
        productIdentityLabel(
          "0.6.14",
          "abc1234ef567890abc1234ef567890abc1234ef5",
        ),
      ).toBe("0.6.14 · abc1234");
    });

    it("does not mix malformed or missing revisions into the version", () => {
      expect(productIdentityLabel("0.6.14", "")).toBe("0.6.14");
      expect(productIdentityLabel("0.6.14", "unknown")).toBe("0.6.14");
      expect(productIdentityLabel("0.6.14", "abc1234")).toBe("0.6.14");
      expect(productIdentityLabel("", "abc1234")).toBe("");
    });

    it("uses the same formatter for the compile-time fallback", () => {
      expect(appDeployLabel).toBe(productIdentityLabel(appVersion, ""));
    });
  });
});
