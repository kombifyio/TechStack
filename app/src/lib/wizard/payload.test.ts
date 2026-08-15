import { describe, expect, it } from "vitest";
import {
  applyServerProvisioningMode,
  buildPayload,
  createDefaultConfig,
  deriveServicesFromGoals,
} from "./index";

describe("wizard identity service decisions", () => {
  it("defaults to Pocket ID without enabling PocketBase", () => {
    const config = createDefaultConfig();

    expect(config.identity).toEqual({
      toolProvider: "pocket-id",
      homelabProvider: "pocket-id",
      requiresPasskeys: false,
      backendCapability: "external-identity",
    });
    expect(config.services.pocketId).toBe(true);
    expect(config.services.pocketbase).toBe(false);
    expect(config.owner.source).toBe("local");
    expect(config.owner.recoveryPassphraseHash).toBe("");

    const payload = buildPayload(config);

    expect(payload.provider).toBe("local");
    expect(payload.services).toEqual(
      expect.arrayContaining([
        "pocket-id",
        "traefik",
        "vaultwarden",
        "otel-collector",
      ]),
    );
    expect(payload.services).not.toContain("monitoring");
    expect(payload.options.server_mode).toBe("user-owned");
    expect(payload.options.server_provisioning_mode).toBe("install-command");
    expect(payload.options.server_connection_mode).toBe("agent-oneliner");
    expect(payload.options.stackkit_foundation).toBe("basement-kit");
    expect(payload.options.server_node_role).toBe("foundation");
    expect(payload.options.server_node_role_wire).toBe("standalone");
    expect(payload.options.server_install_command_required).toBe(true);
    expect(payload.options.runtime_lane).toBeUndefined();
    expect(payload.options.runtime_offering_id).toBeUndefined();
    expect(payload.options.provider_id).toBeUndefined();
    expect(payload.options.ionos_datacenter).toBeUndefined();
    expect(payload.options.provider_region).toBeUndefined();
    expect(payload.options.simulate_node_lifecycle).toBeUndefined();
    expect(payload.options.billing_mode).toBe("local");
    expect(payload.options.billing_cadence).toBeUndefined();
    expect(payload.options.stackkit_catalog_ref).toBe("basement-kit");
    expect(payload.options.use_cases).toEqual(["vault"]);
    expect(payload.options.verification_status).toBe("pending");
    expect(payload.services).toContain("pocket-id");
    expect(payload.services).not.toContain("pocketbase");
    expect(payload.options.enable_pocket_id).toBe(true);
    expect(payload.options.enable_pocketbase).toBe(false);
    expect(payload.options.enable_pocketbase_backend).toBe(false);
    expect(payload.options.enable_monitoring).toBe(true);
    expect(payload.options.identity_head).toBe("pocket_id");
    expect(payload.options.tool_identity_provider).toBe("pocket-id");
    expect(payload.options.homelab_identity_provider).toBe("pocket-id");
    expect(payload.options.owner_bootstrap_mode).toBe("custom");
    expect(payload.options.owner_source).toBe("local");
    expect(payload.options.owner_email).toBeUndefined();
    expect(payload.options.owner_username).toBeUndefined();
    expect(payload.options.recovery_passphrase_hash).toBeUndefined();
  });

  it("expands the all-use-cases preset into canonical StackKits slugs", () => {
    const config = createDefaultConfig();
    config.goals = {
      "smart-home": false,
      photos: false,
      media: false,
      vault: false,
      files: false,
      ai: false,
      dev: false,
      mail: false,
      game: false,
      everything: true,
    };

    const payload = buildPayload(config);

    expect(payload.options.use_cases).toEqual([
      "photos",
      "media",
      "vault",
      "files",
      "smart-home",
      "ai",
      "dev",
      "mail",
      "game",
    ]);
  });

  it("carries registry foundation and server role through the payload contract", () => {
    const config = createDefaultConfig();
    config.serverProvisioning.stackkitFoundation = "cloud-kit";
    config.serverProvisioning.nodeRole = "worker";

    const payload = buildPayload(config);

    expect(payload.options.stackkit_foundation).toBe("cloud-kit");
    expect(payload.options.stackkit_catalog_ref).toBe("cloud-kit");
    expect(payload.options.server_node_role).toBe("worker");
    expect(payload.options.server_node_role_wire).toBe("worker");
  });

  it("sends SaaS managed runtime owner bootstrap without owner or manual recovery fields", () => {
    const config = createDefaultConfig("saas");

    const payload = buildPayload(config);

    expect(payload.provider).toBe("cloud");
    expect(payload.options.server_mode).toBe("monthly-runtime");
    expect(payload.options.server_provisioning_mode).toBe("kombify-cloud");
    expect(payload.options.server_connection_mode).toBe("managed-subscription");
    expect(payload.options.stackkit_foundation).toBe("cloud-kit");
    expect(payload.options.stackkit_catalog_ref).toBe("cloud-kit");
    expect(payload.options.server_install_command_required).toBeUndefined();
    expect(payload.options.runtime_lane).toBe("monthly-runtime");
    expect(payload.options.runtime_offering_id).toBe(
      "monthly-runtime-standard",
    );
    expect(payload.options.provider_id).toBe("centron");
    expect(payload.options.ionos_datacenter).toBeUndefined();
    expect(payload.options.provider_region).toBeUndefined();
    expect(payload.options).not.toHaveProperty("lease_provider");
    expect(payload.options).not.toHaveProperty("simulate_provider_id");
    expect(payload.options.simulate_node_lifecycle).toBe("pvm");
    expect(payload.options.billing_mode).toBe("subscription");
    expect(payload.options.billing_cadence).toBe("monthly");
    expect(payload.options.owner_bootstrap_mode).toBe("auto");
    expect(payload.options.owner_source).toBe("cloud");
    expect(payload.options.owner_email).toBeUndefined();
    expect(payload.options.owner_username).toBeUndefined();
    expect(payload.options.recovery_passphrase_hash).toBeUndefined();
    expect(payload.options.recovery_material_ref).toBe(
      "techstack://recovery/stacks/homelab",
    );
  });

  it("carries remote server connect settings without monthly runtime provider fields", () => {
    const config = createDefaultConfig();
    config.serverProvisioning.mode = "connect-remote";
    config.serverProvisioning.remote.host = "server.example.com";
    config.serverProvisioning.remote.sshUser = "root";
    config.serverProvisioning.remote.sshPort = 2222;
    config.serverProvisioning.remote.authMethod = "ssh-key";

    const payload = buildPayload(config);

    expect(payload.provider).toBe("local");
    expect(payload.options.server_mode).toBe("user-owned");
    expect(payload.options.server_provisioning_mode).toBe("connect-remote");
    expect(payload.options.server_connection_mode).toBe("remote-ssh");
    expect(payload.options.server_remote_host).toBe("server.example.com");
    expect(payload.options.server_remote_user).toBe("root");
    expect(payload.options.server_remote_port).toBe(2222);
    expect(payload.options.server_remote_auth_method).toBe("ssh-key");
    expect(payload.options.runtime_lane).toBeUndefined();
    expect(payload.options.runtime_offering_id).toBeUndefined();
    expect(payload.options.provider_id).toBeUndefined();
    expect(payload.options.billing_mode).toBe("local");
  });

  it("carries the selected IONOS monthly-runtime provider through the payload", () => {
    const config = createDefaultConfig();
    applyServerProvisioningMode(config, "kombify-cloud");
    config.providerId = "ionos";
    config.ionosDatacenter = "us/ewr";

    const payload = buildPayload(config);

    expect(payload.provider).toBe("cloud");
    expect(payload.options.server_mode).toBe("monthly-runtime");
    expect(payload.options.server_provisioning_mode).toBe("kombify-cloud");
    expect(payload.options.server_connection_mode).toBe("managed-subscription");
    expect(payload.options.stackkit_foundation).toBe("cloud-kit");
    expect(payload.options.stackkit_catalog_ref).toBe("cloud-kit");
    expect(payload.options.runtime_lane).toBe("monthly-runtime");
    expect(payload.options.runtime_offering_id).toBe(
      "monthly-runtime-standard",
    );
    expect(payload.options.provider_id).toBe("ionos");
    expect(payload.options.ionos_datacenter).toBe("us/ewr");
    expect(payload.options.provider_region).toBe("us/ewr");
    expect(payload.options).not.toHaveProperty("lease_provider");
    expect(payload.options).not.toHaveProperty("simulate_provider_id");
    expect(payload.options.simulate_node_lifecycle).toBe("pvm");
    expect(payload.options.billing_mode).toBe("subscription");
    expect(payload.options.billing_cadence).toBe("monthly");
    expect(payload.options.owner_bootstrap_mode).toBe("auto");
    expect(payload.options.owner_source).toBe("cloud");
    expect(payload.options.owner_email).toBeUndefined();
    expect(payload.options.owner_username).toBeUndefined();
    expect(payload.options.recovery_passphrase_hash).toBeUndefined();
  });

  it("marks the one-liner path as agent install without direct server fields", () => {
    const config = createDefaultConfig();
    config.serverProvisioning.mode = "install-command";

    const payload = buildPayload(config);

    expect(payload.provider).toBe("local");
    expect(payload.options.server_mode).toBe("user-owned");
    expect(payload.options.server_provisioning_mode).toBe("install-command");
    expect(payload.options.server_connection_mode).toBe("agent-oneliner");
    expect(payload.options.server_install_command_required).toBe(true);
    expect(payload.options.server_remote_host).toBeUndefined();
    expect(payload.options.runtime_lane).toBeUndefined();
    expect(payload.options.billing_cadence).toBeUndefined();
  });

  it("carries owner bootstrap fields through the payload contract", () => {
    const config = createDefaultConfig();
    config.owner.source = "local";
    config.owner.email = "owner@example.com";
    config.owner.username = "owner";
    config.owner.displayName = "Owner";
    config.owner.recoveryPassphraseHash =
      "$argon2id$v=19$m=65536,t=3,p=4$demo$signed";

    const payload = buildPayload(config);

    expect(payload.options.owner_bootstrap_mode).toBe("custom");
    expect(payload.options.owner_source).toBe("local");
    expect(payload.options.owner_email).toBe("owner@example.com");
    expect(payload.options.owner_username).toBe("owner");
    expect(payload.options.owner_display_name).toBe("Owner");
    expect(payload.options.recovery_passphrase_hash).toBe(
      "$argon2id$v=19$m=65536,t=3,p=4$demo$signed",
    );
  });

  it("enables PocketBase only when selected as the homelab backend", () => {
    const config = createDefaultConfig();
    config.identity.homelabProvider = "pocketbase";

    deriveServicesFromGoals(config);
    const payload = buildPayload(config);

    expect(config.services.pocketId).toBe(true);
    expect(config.services.pocketbase).toBe(true);
    expect(payload.services).toEqual(
      expect.arrayContaining(["pocket-id", "pocketbase"]),
    );
    expect(payload.options.enable_pocket_id).toBe(true);
    expect(payload.options.enable_pocketbase).toBe(true);
    expect(payload.options.enable_pocketbase_backend).toBe(true);
    expect(payload.options.identity_head).toBe("pocketbase");
    expect(payload.options.homelab_identity_provider).toBe("pocketbase");
  });

  it("keeps passkeys on Pocket ID without enabling PocketBase by default", () => {
    const config = createDefaultConfig();
    config.identity.requiresPasskeys = true;

    deriveServicesFromGoals(config);
    const payload = buildPayload(config);

    expect(config.services.pocketId).toBe(true);
    expect(config.services.pocketbase).toBe(false);
    expect(payload.services).toContain("pocket-id");
    expect(payload.services).not.toContain("pocketbase");
    expect(payload.options.enable_pocketbase_backend).toBe(false);
    expect(payload.options.identity_head).toBe("pocket_id");
    expect(payload.options.requires_passkeys).toBe(true);
    expect(payload.options.identity_backend_capability).toBe("passkeys");
  });

  it("combines PocketBase backend with Pocket ID when passkeys are required", () => {
    const config = createDefaultConfig();
    config.identity.homelabProvider = "pocketbase";
    config.identity.requiresPasskeys = true;

    deriveServicesFromGoals(config);
    const payload = buildPayload(config);

    expect(config.services.pocketId).toBe(true);
    expect(config.services.pocketbase).toBe(true);
    expect(payload.services).toEqual(
      expect.arrayContaining(["pocket-id", "pocketbase"]),
    );
    expect(payload.options.enable_pocketbase_backend).toBe(true);
    expect(payload.options.identity_head).toBe("pocketbase");
    expect(payload.options.requires_passkeys).toBe(true);
  });
});
