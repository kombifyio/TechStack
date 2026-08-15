import { describe, expect, it } from "vitest";
import { createDefaultConfig } from "./types";
import {
  buildFoundRunRequest,
  buildJoinRunRequest,
  buildRunOwnerOptions,
  normalizeRunKitSlug,
} from "./wizardRunRequest";
import { WIZARD_INTENT_SCHEMA } from "$lib/api/wizardRuns";

describe("buildFoundRunRequest", () => {
  it("maps a self-host first run onto the closed intent contract", () => {
    const config = createDefaultConfig();
    config.name = "My Homelab";
    config.goals!.photos = true;
    config.goals!["smart-home"] = true;
    config.owner.bootstrapMode = "custom";
    config.owner.source = "local";
    config.owner.email = "owner@example.com";
    config.owner.username = "owner";
    config.owner.displayName = "Owner";
    config.owner.recoveryPassphraseHash = "$argon2id$test";

    const request = buildFoundRunRequest(config);

    expect(request.intent.schema).toBe(WIZARD_INTENT_SCHEMA);
    expect(request.intent.run_kind).toBe("first-run");
    expect(request.intent.name).toBe("My Homelab");
    expect(request.intent.goals).toEqual(
      expect.arrayContaining(["photos", "smart-home"]),
    );
    expect(request.intent.kit_assignment).toEqual({
      mode: "found",
      kit_slug: "basement-kit",
    });
    expect(request.intent.server.transport).toBe("install-command");
    expect(request.intent.server.roles).toEqual(["foundation"]);
    expect(request.owner).toEqual({
      owner_bootstrap_mode: "custom",
      owner_source: "local",
      owner_email: "owner@example.com",
      owner_username: "owner",
      owner_display_name: "Owner",
      recovery_passphrase_hash: "$argon2id$test",
    });
    expect(request.managed).toBeUndefined();
    expect(request.remote).toBeUndefined();
    // First runs send the payload wire names (same vocabulary as the legacy
    // create request).
    expect(request.services).toContain("pocket-id");
  });

  it("defaults the name and expands the everything goal", () => {
    const config = createDefaultConfig();
    config.name = "";
    config.goals!.everything = true;

    const request = buildFoundRunRequest(config);

    expect(request.intent.name).toBe("homelab");
    expect(request.intent.goals).toContain("photos");
    expect(request.intent.goals).toContain("game");
    expect(request.intent.goals).not.toContain("everything");
  });

  it("omits owner identity fields for the cloud-linked source", () => {
    const config = createDefaultConfig();
    config.owner.bootstrapMode = "custom";
    config.owner.source = "cloud-linked";
    config.owner.email = "must-not-travel@example.com";
    config.owner.username = "must-not-travel";

    const owner = buildRunOwnerOptions(config);

    expect(owner).toBeDefined();
    expect(owner!.owner_source).toBe("cloud-linked");
    expect(owner!.owner_email).toBeUndefined();
    expect(owner!.owner_username).toBeUndefined();
    expect(owner!.owner_display_name).toBeUndefined();
  });

  it("omits the owner section entirely for bootstrap mode none", () => {
    const config = createDefaultConfig();
    config.owner.bootstrapMode = "none";
    expect(buildRunOwnerOptions(config)).toBeUndefined();
  });

  it("maps the managed lane with IONOS region fields", () => {
    const config = createDefaultConfig();
    config.serverProvisioning.mode = "kombify-cloud";
    config.providerId = "ionos";
    config.runtimeOfferingId = "monthly-runtime-standard";
    config.ionosDatacenter = "de/txl";

    const request = buildFoundRunRequest(config);

    expect(request.intent.server.transport).toBe("kombify-cloud");
    expect(request.managed).toEqual({
      provider_id: "ionos",
      runtime_offering_id: "monthly-runtime-standard",
      ionos_datacenter: "de/txl",
      provider_region: "de/txl",
    });
  });

  it("maps connect-remote SSH fields", () => {
    const config = createDefaultConfig();
    config.serverProvisioning.mode = "connect-remote";
    config.serverProvisioning.remote.host = "10.0.0.5";
    config.serverProvisioning.remote.sshPort = 2222;
    config.serverProvisioning.remote.sshUser = "ubuntu";
    config.serverProvisioning.remote.authMethod = "ssh-key";
    config.serverProvisioning.remote.sshKeyLabel = "homelab-key";
    config.serverProvisioning.remote.useSudo = true;

    const request = buildFoundRunRequest(config);

    expect(request.intent.server.transport).toBe("connect-remote");
    expect(request.remote).toEqual({
      host: "10.0.0.5",
      port: 2222,
      user: "ubuntu",
      auth_method: "ssh-key",
      ssh_key_label: "homelab-key",
      use_sudo: true,
    });
  });
});

describe("normalizeRunKitSlug", () => {
  it("normalizes the legacy base-kit onto an installable slug", () => {
    const config = createDefaultConfig();
    config.serverProvisioning.stackkitFoundation = "base-kit";
    config.kit = "base-kit";
    expect(normalizeRunKitSlug(config)).toBe("basement-kit");
  });

  it("defaults the managed lane to cloud-kit for legacy kit refs", () => {
    const config = createDefaultConfig();
    config.serverProvisioning.mode = "kombify-cloud";
    config.serverProvisioning.stackkitFoundation = "base-kit";
    config.kit = "base-kit";
    expect(normalizeRunKitSlug(config)).toBe("cloud-kit");
  });
});

describe("buildJoinRunRequest", () => {
  it("builds an expansion join without an owner section", () => {
    const config = createDefaultConfig();
    config.serverProvisioning.nodeRole = "worker";
    config.serverProvisioning.mode = "install-command";

    const request = buildJoinRunRequest(config, "stack-1", "my-homelab", [
      "monitoring",
    ]);

    expect(request.intent.run_kind).toBe("expansion");
    expect(request.intent.name).toBe("my-homelab");
    expect(request.intent.kit_assignment).toEqual({
      mode: "join",
      kit_deployment_id: "stack-1",
    });
    expect(request.intent.server.roles).toEqual(["worker"]);
    expect(request.owner).toBeUndefined();
    // Joins keep the StackConfig service keys the legacy pairing lane sent.
    expect(request.services).toEqual(["monitoring"]);
  });
});
