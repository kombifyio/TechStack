import { describe, expect, it } from "vitest";
import {
  ACTIVE_STANDARD_BUNDLE,
  ADVANCED_USE_CASE_GOALS,
  CANONICAL_USE_CASE_GOALS,
  PRIMARY_USE_CASE_GOALS,
  USE_CASE_FEATURE_FLAGS,
  buildStackKitServicesFromBundle,
  buildStackSpecServiceTogglesFromBundle,
  getIonosDatacenterChoices,
  getRegistryNodeRoleChoices,
  getServerProvisioningModeChoices,
  getStackKitFoundationChoices,
  getTechieServiceQuestions,
  isDiscoveryEnabledForWizard,
  serviceConfigKeysFromBundle,
  selectedCanonicalUseCasesFromGoals,
} from "./standardBundle";
import { createDefaultConfig, EASY_STEPS, TECHIE_STEPS } from "./types";

describe("active StandardBundle wizard definition", () => {
  it("owns wizard steps and Basement Kit defaults", () => {
    const config = createDefaultConfig();

    expect(ACTIVE_STANDARD_BUNDLE.id).toBe("basement-kit.standard.v1");
    expect(EASY_STEPS).toEqual(ACTIVE_STANDARD_BUNDLE.wizard.steps.easy);
    expect(TECHIE_STEPS).toEqual(ACTIVE_STANDARD_BUNDLE.wizard.steps.techie);
    expect(config.kit).toBe(ACTIVE_STANDARD_BUNDLE.kit);
    expect(config.serverMode).toBe(ACTIVE_STANDARD_BUNDLE.defaults.serverMode);
    expect(config.services.vaultwarden).toBe(true);
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
        "vaultwarden",
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
      vaultwarden: true,
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
    expect(selectedCanonicalUseCasesFromGoals(config.goals)).toEqual(["vault"]);
    expect(
      selectedCanonicalUseCasesFromGoals({
        ...config.goals,
        everything: true,
      }),
    ).toEqual(CANONICAL_USE_CASE_GOALS);
  });

  it("gates Discovery out of wizards and renders choices from bundle questions", () => {
    expect(isDiscoveryEnabledForWizard("easy")).toBe(false);
    expect(isDiscoveryEnabledForWizard("techie")).toBe(false);
    expect(
      ACTIVE_STANDARD_BUNDLE.wizard.steps.easy.map((step) => step.key),
    ).not.toContain("discovery");
    expect(
      ACTIVE_STANDARD_BUNDLE.wizard.steps.techie.map((step) => step.key),
    ).not.toContain("discovery");
    const serverProvisioningModes = getServerProvisioningModeChoices();
    expect(serverProvisioningModes.map((mode) => mode.value)).toEqual([
      "install-command",
      "connect-remote",
      "kombify-cloud",
    ]);
    expect(serverProvisioningModes.map((mode) => mode.testId)).toEqual(
      expect.arrayContaining([
        "server-mode-install-command",
        "server-mode-connect-remote",
        "server-mode-kombify-cloud",
      ]),
    );
    expect(
      serverProvisioningModes.some(
        (mode) =>
          "disabled" in mode ||
          "featureFlag" in mode ||
          mode.badgeKey === "common.comingSoon",
      ),
    ).toBe(false);
    expect(
      getStackKitFoundationChoices().map((foundation) => foundation.value),
    ).toEqual(["basement-kit", "cloud-kit"]);
    expect(getRegistryNodeRoleChoices().map((role) => role.value)).toEqual([
      "foundation",
      "worker",
      "storage",
    ]);
    expect(
      getIonosDatacenterChoices().map((datacenter) => datacenter.value),
    ).toEqual(["de/fra", "de/txl", "us/ewr"]);
    expect(
      getTechieServiceQuestions().map((service) => service.testId),
    ).toEqual(
      expect.arrayContaining([
        "techie-svc-pocketbase",
        "techie-svc-traefik",
        "techie-svc-vaultwarden",
        "techie-svc-immich",
      ]),
    );
  });
});
