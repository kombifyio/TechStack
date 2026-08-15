import { describe, expect, it } from "vitest";
import {
  applyServerProvisioningMode,
  applyWizardDeploymentLane,
  createDefaultConfig,
  EASY_STEPS,
  normalizeIonosDatacenter,
  TECHIE_STEPS,
} from "./types";
import { ACTIVE_STANDARD_BUNDLE } from "./standardBundle";

describe("easy wizard stage contract", () => {
  it("uses StandardBundle steps for Easy and Techie surfaces", () => {
    expect(EASY_STEPS).toEqual(ACTIVE_STANDARD_BUNDLE.wizard.steps.easy);
    expect(TECHIE_STEPS).toEqual(ACTIVE_STANDARD_BUNDLE.wizard.steps.techie);
    expect(EASY_STEPS.map((step) => step.key)).toEqual([
      "goals",
      "server",
      "access",
      "users",
      "login",
    ]);
  });
});

describe("basementkit verified wizard defaults", () => {
  it("defaults Easy Wizard to the user-owned one-liner StackKit rollout", () => {
    const config = createDefaultConfig();

    expect(config.provider).toBe("local");
    expect(config.serverMode).toBe("user-owned");
    expect(config.serverProvisioning.mode).toBe("install-command");
    expect(config.serverProvisioning.connectionMode).toBe("agent-oneliner");
    expect(config.serverProvisioning.stackkitFoundation).toBe("basement-kit");
    expect(config.serverProvisioning.nodeRole).toBe("foundation");
    expect(config.owner.bootstrapMode).toBe("custom");
    expect(config.kit).toBe("basement-kit");
    expect(config.providerId).toBe("centron");
    expect(config.ionosDatacenter).toBe("de/fra");
    expect(config.services.pocketId).toBe(true);
    expect(config.services.traefik).toBe(true);
    expect(config.services.vaultwarden).toBe(true);
    expect(config.services.immich).toBe(false);
    expect(config.services.monitoring).toBe(true);
    expect(config.services.files).toBe(false);
  });

  it("maps server provisioning choices onto orchestration runtime mode", () => {
    const config = createDefaultConfig();

    applyServerProvisioningMode(config, "connect-remote");
    expect(config.provider).toBe("local");
    expect(config.serverMode).toBe("user-owned");
    expect(config.serverProvisioning.mode).toBe("connect-remote");

    applyServerProvisioningMode(config, "install-command");
    expect(config.provider).toBe("local");
    expect(config.serverMode).toBe("user-owned");
    expect(config.serverProvisioning.mode).toBe("install-command");

    applyServerProvisioningMode(config, "kombify-cloud");
    expect(config.provider).toBe("cloud");
    expect(config.serverMode).toBe("monthly-runtime");
    expect(config.kit).toBe("cloud-kit");
    expect(config.serverProvisioning.stackkitFoundation).toBe("cloud-kit");
    expect(config.providerId).toBe("centron");
    expect(config.runtimeOfferingId).toBe("monthly-runtime-standard");
    expect(config.owner.bootstrapMode).toBe("auto");
    expect(config.owner.source).toBe("cloud");
    expect(config.owner.username).toBe("");
    expect(config.owner.email).toBe("");
    expect(config.owner.recoveryPassphraseHash).toBe("");
    expect(config.identity.homelabProvider).toBe("pocket-id");
    expect(config.identity.requiresPasskeys).toBe(true);
  });

  it("preserves the controlled managed VPS provider when normalizing cloud mode", () => {
    const config = createDefaultConfig();
    config.providerId = "ionos";
    config.owner.username = "local-owner";
    config.owner.email = "owner@example.com";
    config.owner.recoveryPassphraseHash =
      "$argon2id$v=19$m=65536,t=3,p=4$demo$signed";

    applyServerProvisioningMode(config, "kombify-cloud");

    expect(config.provider).toBe("cloud");
    expect(config.serverMode).toBe("monthly-runtime");
    expect(config.providerId).toBe("ionos");
    expect(config.ionosDatacenter).toBe("de/fra");
    expect(config.owner.bootstrapMode).toBe("auto");
    expect(config.owner.source).toBe("cloud");
    expect(config.owner.username).toBe("");
    expect(config.owner.email).toBe("");
    expect(config.owner.recoveryPassphraseHash).toBe("");
  });

  it("defaults the SaaS lane to managed runtime with automatic owner bootstrap", () => {
    const config = createDefaultConfig("saas");

    expect(config.provider).toBe("cloud");
    expect(config.serverMode).toBe("monthly-runtime");
    expect(config.serverProvisioning.mode).toBe("kombify-cloud");
    expect(config.serverProvisioning.stackkitFoundation).toBe("cloud-kit");
    expect(config.kit).toBe("cloud-kit");
    expect(config.serverProvisioning.connectionMode).toBe(
      "managed-subscription",
    );
    expect(config.providerId).toBe("centron");
    expect(config.runtimeOfferingId).toBe("monthly-runtime-standard");
    expect(config.owner.bootstrapMode).toBe("auto");
    expect(config.owner.source).toBe("cloud");
    expect(config.owner.email).toBe("");
    expect(config.owner.username).toBe("");
    expect(config.owner.recoveryPassphraseHash).toBe("");
    expect(config.owner.recoveryMaterialRef).toBe(
      "techstack://recovery/stacks/homelab",
    );
    expect(config.identity.homelabProvider).toBe("pocket-id");
    expect(config.identity.requiresPasskeys).toBe(true);
  });

  it("can switch an existing config into the SaaS lane without custom owner fields", () => {
    const config = createDefaultConfig();
    config.owner.email = "old@example.com";
    config.owner.username = "old";

    applyWizardDeploymentLane(config, "saas");

    expect(config.serverProvisioning.mode).toBe("kombify-cloud");
    expect(config.serverMode).toBe("monthly-runtime");
    expect(config.provider).toBe("cloud");
    expect(config.owner.bootstrapMode).toBe("auto");
    expect(config.owner.source).toBe("cloud");
    expect(config.owner.email).toBe("");
    expect(config.owner.username).toBe("");
  });

  it("keeps user-owned self-hosted provisioning selectable after SaaS defaults", () => {
    const config = createDefaultConfig("saas");

    applyServerProvisioningMode(config, "install-command");
    expect(config.provider).toBe("local");
    expect(config.serverMode).toBe("user-owned");
    expect(config.serverProvisioning.mode).toBe("install-command");
    expect(config.serverProvisioning.connectionMode).toBe("agent-oneliner");
    expect(config.serverProvisioning.stackkitFoundation).toBe("basement-kit");
    expect(config.kit).toBe("basement-kit");

    applyServerProvisioningMode(config, "connect-remote");
    expect(config.provider).toBe("local");
    expect(config.serverMode).toBe("user-owned");
    expect(config.serverProvisioning.mode).toBe("connect-remote");
    expect(config.serverProvisioning.connectionMode).toBe("remote-ssh");
  });
});

describe("IONOS datacenter normalization", () => {
  it("maps supported location aliases onto stable IONOS location IDs", () => {
    expect(normalizeIonosDatacenter(undefined)).toBe("de/fra");
    expect(normalizeIonosDatacenter("frankfurt")).toBe("de/fra");
    expect(normalizeIonosDatacenter("de-txl")).toBe("de/txl");
    expect(normalizeIonosDatacenter("newark")).toBe("us/ewr");
  });
});
