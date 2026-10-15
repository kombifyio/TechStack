import { describe, expect, it } from "vitest";
import {
  ACTIVE_STANDARD_BUNDLE,
  isDiscoveryEnabledForWizard,
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

  it("keeps the template marker out of explicit canonical selection", () => {
    const config = createDefaultConfig();
    expect(selectedCanonicalUseCasesFromGoals(config.goals)).toEqual([]);
    expect(
      selectedCanonicalUseCasesFromGoals({
        ...config.goals,
        everything: true,
      }),
    ).toEqual([]);
    expect(
      selectedCanonicalUseCasesFromGoals({
        ...config.goals,
        photos: true,
        everything: true,
      }),
    ).toEqual(["photos"]);
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
