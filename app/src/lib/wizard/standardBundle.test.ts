import { describe, expect, it } from "vitest";
import {
  ACTIVE_STANDARD_BUNDLE,
  ADVANCED_USE_CASE_GOALS,
  CANONICAL_USE_CASE_GOALS,
  PRIMARY_USE_CASE_GOALS,
  USE_CASE_FEATURE_FLAGS,
  buildStackKitServicesFromBundle,
  buildStackSpecServiceTogglesFromBundle,
  isDiscoveryEnabledForWizard,
  serviceConfigKeysFromBundle,
  selectedCanonicalUseCasesFromGoals,
} from "./standardBundle";
import { createDefaultConfig } from "./types";

describe("active StandardBundle wizard definition", () => {
  it("owns Basement Kit defaults", () => {
    const config = createDefaultConfig();

    expect(ACTIVE_STANDARD_BUNDLE.id).toBe("basement-kit.standard.v1");
    expect(config.kit).toBe(ACTIVE_STANDARD_BUNDLE.kit);
    expect(config.serverMode).toBe(ACTIVE_STANDARD_BUNDLE.defaults.serverMode);
    expect(config.services.vaultwarden).toBe(false);
    expect(config.services.immich).toBe(false);
  });

  it("uses bundle service definitions for StackKit service specs", () => {
    const config = createDefaultConfig();
    const services = buildStackKitServicesFromBundle(config.services);
    const toggles = buildStackSpecServiceTogglesFromBundle(config.services);

    expect(serviceConfigKeysFromBundle()).toEqual(
      expect.arrayContaining(["pocketId", "traefik", "vaultwarden", "immich"]),
    );
    expect(services.map((service) => service.name)).toEqual(
      expect.arrayContaining([
        "pocket-id",
        "traefik",
        "otel-collector",
      ]),
    );
    expect(toggles).toMatchObject({
      homepage: true,
      whoami: true,
      tinyauth: true,
      pocketid: true,
      traefik: true,
      "uptime-kuma": true,
      vaultwarden: false,
      immich: false,
      pocketbase: false,
      files: false,
    });
  });

  it("exposes the canonical StackKits use cases and expands the everything preset", () => {
    const config = createDefaultConfig();

    expect(
      ACTIVE_STANDARD_BUNDLE.wizard.goals.map((goal) => goal.configKey),
    ).toEqual([
      ...PRIMARY_USE_CASE_GOALS,
      "everything",
      ...ADVANCED_USE_CASE_GOALS,
    ]);
    expect(CANONICAL_USE_CASE_GOALS).not.toContain("remote");
    expect(
      ACTIVE_STANDARD_BUNDLE.wizard.goals
        .filter((goal) => goal.surface === "advanced")
        .map((goal) => goal.configKey),
    ).toEqual(ADVANCED_USE_CASE_GOALS);
    expect(USE_CASE_FEATURE_FLAGS.mail).toBe("use_case_mail");
    expect(selectedCanonicalUseCasesFromGoals(config.goals)).toEqual([]);
    expect(
      selectedCanonicalUseCasesFromGoals({
        ...config.goals,
        everything: true,
      }),
    ).toEqual(CANONICAL_USE_CASE_GOALS);
  });

  // The invariant is that Discovery never became a wizard step. The catalog
  // contents around it - provisioning modes, foundations, node roles,
  // datacenters, service questions - are product inventory that changes on
  // purpose, so this does not enumerate them.
  it("keeps Discovery out of the wizard", () => {
    expect(isDiscoveryEnabledForWizard("easy")).toBe(false);
    expect(
      ACTIVE_STANDARD_BUNDLE.wizard.steps.easy.map((step) => step.key),
    ).not.toContain("discovery");
  });
});
