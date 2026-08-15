/**
 * Feature Flags Store Unit Tests
 *
 * Tests for the feature flags store to ensure:
 * - Proper default values before loading
 * - Ready state tracking
 * - Correct behavior of derived stores
 * - Default values match backend definitions
 */
import { describe, it, expect, beforeEach, vi } from "vitest";
import { get } from "svelte/store";
import { getDefaultEnabled } from "./features";

// Note: Full store testing requires mocking the API client.
// These tests focus on the pure functions and default logic.

describe("Feature Flag Defaults", () => {
  describe("getDefaultEnabled", () => {
    it("returns false for security features (security-by-default)", () => {
      // These features should ALWAYS default to OFF
      expect(getDefaultEnabled("network_discovery")).toBe(false);
      expect(getDefaultEnabled("raw_commands")).toBe(false);
      expect(getDefaultEnabled("ssh_tunnel")).toBe(false);
      expect(getDefaultEnabled("cloud_backup")).toBe(false);
    });

    it("returns false for beta features (opt-in only)", () => {
      // Beta features should default to OFF
      expect(getDefaultEnabled("cloudflare_tunnel")).toBe(false);
      expect(getDefaultEnabled("self_healing")).toBe(false);
      expect(getDefaultEnabled("ha_stackkit")).toBe(false);
    });

    it("returns true for UX features (usability-by-default)", () => {
      // UX features should default to ON
      expect(getDefaultEnabled("onboarding_wizard")).toBe(true);
      expect(getDefaultEnabled("keyboard_shortcuts")).toBe(true);
      expect(getDefaultEnabled("dark_mode")).toBe(true);
      expect(getDefaultEnabled("use_case_photos")).toBe(true);
      expect(getDefaultEnabled("use_case_mail")).toBe(true);
    });

    it("returns false for unknown features (fail-safe)", () => {
      // Unknown features should default to OFF for security
      expect(getDefaultEnabled("unknown_feature")).toBe(false);
      expect(getDefaultEnabled("")).toBe(false);
      expect(getDefaultEnabled("some_random_key")).toBe(false);
    });
  });

  describe("Default consistency with backend", () => {
    // These tests verify the frontend FEATURE_DEFAULTS match
    // the backend Go definitions in pkg/features/flags.go
    // If these fail, it means frontend/backend are out of sync!

    it("security features match backend SecurityFeatures defaults", () => {
      // From pkg/features/flags.go SecurityFeatures
      const backendSecurityDefaults: Record<string, boolean> = {
        network_discovery: false, // DefaultValue: false
        raw_commands: false, // DefaultValue: false
        ssh_tunnel: false, // DefaultValue: false
        cloud_backup: false, // DefaultValue: false
      };

      for (const [key, expectedDefault] of Object.entries(
        backendSecurityDefaults,
      )) {
        expect(
          getDefaultEnabled(key),
          `Security feature "${key}" should match backend default`,
        ).toBe(expectedDefault);
      }
    });

    it("beta features match backend BetaFeatures defaults", () => {
      // From pkg/features/flags.go BetaFeatures
      const backendBetaDefaults: Record<string, boolean> = {
        native_v2_wizard: false, // DefaultValue: false
        cloudflare_tunnel: false, // DefaultValue: false
        self_healing: false, // DefaultValue: false
        ha_stackkit: false, // DefaultValue: false
      };

      for (const [key, expectedDefault] of Object.entries(
        backendBetaDefaults,
      )) {
        expect(
          getDefaultEnabled(key),
          `Beta feature "${key}" should match backend default`,
        ).toBe(expectedDefault);
      }
    });

    it("UX features match backend UXFeatures defaults", () => {
      // From pkg/features/flags.go UXFeatures
      const backendUXDefaults: Record<string, boolean> = {
        onboarding_wizard: true, // DefaultValue: true
        keyboard_shortcuts: true, // DefaultValue: true
        dark_mode: true, // DefaultValue: true
        use_case_photos: true, // DefaultValue: true
        use_case_media: true, // DefaultValue: true
        use_case_vault: true, // DefaultValue: true
        use_case_files: true, // DefaultValue: true
        use_case_smart_home: true, // DefaultValue: true
        use_case_ai: true, // DefaultValue: true
        use_case_dev: true, // DefaultValue: true
        use_case_mail: true, // DefaultValue: true
        use_case_game: true, // DefaultValue: true
      };

      for (const [key, expectedDefault] of Object.entries(backendUXDefaults)) {
        expect(
          getDefaultEnabled(key),
          `UX feature "${key}" should match backend default`,
        ).toBe(expectedDefault);
      }
    });
  });
});

describe("Feature Flag Invariants", () => {
  it("no UX feature should ever default to false", () => {
    // Critical invariant: UX features must always be ON by default
    // If this test fails, users will have a broken experience
    const uxFeatures = [
      "onboarding_wizard",
      "keyboard_shortcuts",
      "dark_mode",
      "use_case_photos",
      "use_case_media",
      "use_case_vault",
      "use_case_files",
      "use_case_smart_home",
      "use_case_ai",
      "use_case_dev",
      "use_case_mail",
      "use_case_game",
    ];

    for (const feature of uxFeatures) {
      expect(
        getDefaultEnabled(feature),
        `UX feature "${feature}" MUST default to true`,
      ).toBe(true);
    }
  });

  it("no security feature should ever default to true", () => {
    // Critical invariant: Security features must be OFF by default
    // If this test fails, dangerous features could be enabled without consent
    const securityFeatures = [
      "network_discovery",
      "raw_commands",
      "ssh_tunnel",
      "cloud_backup",
    ];

    for (const feature of securityFeatures) {
      expect(
        getDefaultEnabled(feature),
        `Security feature "${feature}" MUST default to false`,
      ).toBe(false);
    }
  });
});
