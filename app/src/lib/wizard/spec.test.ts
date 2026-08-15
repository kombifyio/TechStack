import { describe, expect, it } from "vitest";
import {
  buildInputSpecFromStackConfig,
  buildStackKitSpecFromStackConfig,
} from "./spec";
import { ACTIVE_STANDARD_BUNDLE } from "./standardBundle";
import { applyServerProvisioningMode, createDefaultConfig } from "./types";

describe("wizard StackKit spec", () => {
  it("builds a StackKits stack-spec that can replace a StackKit default spec", () => {
    const config = createDefaultConfig();

    const spec = buildStackKitSpecFromStackConfig(config) as {
      name: string;
      stackkit: string;
      mode: string;
      runtime: string;
      context: string;
      domain: string;
      subdomainPrefix?: string;
      compute: { tier: string };
      paas: string;
      useCases: string[];
      nodes: Array<{ name: string; role: string; provider?: string }>;
      services: Record<string, { enabled: boolean }>;
      owner: { bootstrapMode: string; source: string };
      network: { mode: string };
      vpn?: { enabled: boolean; type: string };
      metadata: Record<string, string>;
      email: string;
      adminEmail: string;
    };

    expect(spec.name).toBe("homelab");
    expect(spec.stackkit).toBe("basement-kit");
    expect(spec.mode).toBe("simple");
    expect(spec.runtime).toBe("docker");
    expect(spec.context).toBe("local");
    expect(spec.domain).toBe("stack.home");
    expect(spec.subdomainPrefix).toBeUndefined();
    expect(spec.email).toBe("admin@stack.home");
    expect(spec.adminEmail).toBe("admin@stack.home");
    expect(spec.compute.tier).toBe("standard");
    expect(spec.paas).toBe("dokploy");
    expect(spec.useCases).toEqual(["vault"]);
    expect(spec.nodes).toEqual([{ name: "main", role: "standalone" }]);
    expect(spec.network.mode).toBe("hybrid");
    expect(spec.vpn).toEqual({ enabled: true, type: "headscale" });
    expect(spec.services).toMatchObject({
      homepage: { enabled: true },
      dokploy: { enabled: true },
      "uptime-kuma": { enabled: true },
      whoami: { enabled: true },
      tinyauth: { enabled: true },
      pocketid: { enabled: true },
      vaultwarden: { enabled: true },
      immich: { enabled: false },
    });
    expect(spec.metadata.spec_format).toBe("stack-spec");
    expect(spec.metadata.decision_context_version).toBe(
      "techstack/decision-context/v1",
    );
    expect(spec.metadata.decision_channel).toBe("wizard:easy");
    expect(spec.metadata.operator_capability_score).toBe("3");
    expect(spec.metadata.operator_capability_band).toBe("guided");
    expect(spec.metadata.operator_capability_source).toBe("wizard-derived");
    expect(spec.metadata.operator_capability_evidence).toBe("wizard:easy");
    expect(spec.metadata.advanced_interaction_count).toBe("0");
    expect(spec.metadata.explicit_alternative_count).toBe("0");
    expect(spec.metadata.advanced_settings_touched).toBe("false");
    expect(spec.owner.bootstrapMode).toBe("custom");
    expect(spec.owner.source).toBe("local");
    expect(spec).not.toHaveProperty("goals");
    expect(spec).not.toHaveProperty("serverProvisioning");
    expect(spec).not.toHaveProperty("kit");
  });

  it("builds StackKit service toggles from the active StandardBundle", () => {
    const config = createDefaultConfig();
    const spec = buildStackKitSpecFromStackConfig(config) as {
      services: Record<string, { enabled: boolean }>;
    };

    for (const service of ACTIVE_STANDARD_BUNDLE.stackSpec.coreServices) {
      expect(spec.services[service]).toEqual({ enabled: true });
    }
    for (const service of ACTIVE_STANDARD_BUNDLE.services) {
      for (const toggle of service.stackSpecServices) {
        expect(spec.services[toggle]).toEqual({
          enabled: config.services[service.key],
        });
      }
    }
  });

  it("keeps the legacy buildInputSpec export as the canonical StackKit spec", () => {
    const config = createDefaultConfig();

    expect(buildInputSpecFromStackConfig(config)).toEqual(
      buildStackKitSpecFromStackConfig(config),
    );
  });

  it("defaults the lean-core owner metadata to local without recovery hash", () => {
    const config = createDefaultConfig();

    const spec = buildStackKitSpecFromStackConfig(config) as {
      stackkit: string;
      services: Record<string, { enabled: boolean }>;
      metadata: Record<string, string>;
    };

    expect(spec.stackkit).toBe("basement-kit");
    expect(spec.services.pocketid.enabled).toBe(true);
    expect(spec.services.vaultwarden.enabled).toBe(true);
    expect(spec.services.immich.enabled).toBe(false);
    expect(spec.metadata.server_mode).toBe("user-owned");
    expect(spec.metadata.server_provisioning_mode).toBe("install-command");
    expect(spec.metadata.server_connection_mode).toBe("agent-oneliner");
    expect(spec.metadata.server_install_command_required).toBe("true");
    expect(spec.metadata.server_registry_module).toBe("server-registry");
    expect(spec.metadata.service_registry_module).toBe("service-registry");
    expect(spec.metadata.use_cases).toBe("vault");
    expect(spec.metadata.stackkit_foundation).toBe("basement-kit");
    expect(spec.metadata.foundation_node_label).toBe("Foundation Node");
    expect(spec.metadata.server_node_role).toBe("foundation");
    expect(spec.metadata.server_node_role_wire).toBe("standalone");
    expect(spec.metadata.foundation_node_wire_compat).toBe(
      "main,standalone,control-plane",
    );
    expect(spec.metadata.runtime_lane).toBe("");
    expect(spec.metadata.runtime_offering_id).toBe("");
    expect(spec.metadata.provider_id).toBe("");
    expect(spec.metadata).not.toHaveProperty("lease_provider");
    expect(spec.metadata).not.toHaveProperty("simulate_provider_id");
    expect(spec.metadata.simulate_node_lifecycle).toBe("");
    expect(spec.metadata.billing_mode).toBe("local");
    expect(spec.metadata.billing_cadence).toBe("");
    expect(spec.metadata.stackkit_catalog_ref).toBe("basement-kit");
    expect(spec.metadata.verification_status).toBe("pending");
    expect(spec.metadata.owner_bootstrap_mode).toBe("custom");
    expect(spec.metadata.owner_source).toBe("local");
    expect(spec.metadata.owner_email_present).toBe("false");
    expect(spec.metadata.owner_username_present).toBe("false");
    expect(spec.metadata.owner_display_name_present).toBe("false");
    expect(spec.metadata.recovery_passphrase_hash_present).toBe("false");
  });

  it("binds registry foundation and node role into StackKits output", () => {
    const config = createDefaultConfig();
    config.serverProvisioning.stackkitFoundation = "cloud-kit";
    config.serverProvisioning.nodeRole = "worker";

    const spec = buildStackKitSpecFromStackConfig(config) as {
      stackkit: string;
      nodes: Array<{ name: string; role: string }>;
      metadata: Record<string, string>;
    };

    expect(spec.stackkit).toBe("cloud-kit");
    expect(spec.nodes).toEqual([{ name: "worker-1", role: "worker" }]);
    expect(spec.metadata.stackkit_foundation).toBe("cloud-kit");
    expect(spec.metadata.stackkit_catalog_ref).toBe("cloud-kit");
    expect(spec.metadata.server_node_role).toBe("worker");
    expect(spec.metadata.server_node_role_wire).toBe("worker");
  });

  it("raises capability metadata for Techie Wizard and explicit alternatives", () => {
    const config = createDefaultConfig();
    config.wizardType = "techie";
    config.serverProvisioning.stackkitFoundation = "cloud-kit";
    config.serverProvisioning.nodeRole = "worker";
    config.advanced.backupsEnabled = false;

    const spec = buildStackKitSpecFromStackConfig(config) as {
      metadata: Record<string, string>;
    };

    expect(spec.metadata.decision_channel).toBe("wizard:techie");
    expect(spec.metadata.operator_capability_score).toBe("9");
    expect(spec.metadata.operator_capability_band).toBe("expert");
    expect(spec.metadata.operator_capability_evidence).toContain(
      "wizard:techie",
    );
    expect(spec.metadata.operator_capability_evidence).toContain("advanced:1");
    expect(spec.metadata.operator_capability_evidence).toContain(
      "alternatives:2",
    );
    expect(spec.metadata.advanced_interaction_count).toBe("1");
    expect(spec.metadata.explicit_alternative_count).toBe("2");
    expect(spec.metadata.advanced_settings_touched).toBe("true");
  });

  it("describes direct remote server provisioning without provider internals", () => {
    const config = createDefaultConfig();
    config.serverProvisioning.mode = "connect-remote";
    config.serverProvisioning.remote.host = "server.example.com";
    config.serverProvisioning.remote.sshPort = 2222;
    config.serverProvisioning.remote.sshUser = "root";

    const spec = buildStackKitSpecFromStackConfig(config) as {
      nodes: Array<{
        role: string;
        host?: string;
        ip?: string;
        ssh?: { host: string; port: number; user: string };
      }>;
      ssh: { user: string; port: number };
      context: string;
      network: { mode: string };
      metadata: Record<string, string>;
    };

    expect(spec.context).toBe("local");
    expect(spec.network.mode).toBe("hybrid");
    expect(spec.nodes[0].role).toBe("standalone");
    expect(spec.nodes[0].ip).toBe("server.example.com");
    expect(spec.nodes[0].host).toBe("server.example.com");
    expect(spec.nodes[0].ssh).toEqual({
      host: "server.example.com",
      port: 2222,
      user: "root",
    });
    expect(spec.ssh).toEqual({ user: "root", port: 2222 });
    expect(spec.metadata.server_mode).toBe("user-owned");
    expect(spec.metadata.server_provisioning_mode).toBe("connect-remote");
    expect(spec.metadata.server_connection_mode).toBe("remote-ssh");
    expect(spec.metadata.server_remote_host_present).toBe("true");
    expect(spec.metadata.server_remote_port).toBe("2222");
    expect(spec.metadata.server_remote_user_present).toBe("true");
    expect(spec.metadata.runtime_lane).toBe("");
    expect(spec.metadata.provider_id).toBe("");
    expect(spec.metadata).not.toHaveProperty("lease_provider");
    expect(spec.metadata).not.toHaveProperty("simulate_provider_id");
    expect(spec.metadata.billing_mode).toBe("local");
  });

  it("does not invent owner contact fields for SaaS automatic bootstrap", () => {
    const config = createDefaultConfig("saas");

    const spec = buildStackKitSpecFromStackConfig(config) as {
      mode: string;
      context: string;
      network: { mode: string };
      email?: string;
      adminEmail?: string;
      owner: {
        bootstrapMode: string;
        source: string;
        email?: string;
        username?: string;
        recoveryMaterialRef?: string;
      };
      metadata: Record<string, string>;
    };

    expect(spec.context).toBe("cloud");
    expect(spec.mode).toBe("bootstrapped");
    expect(spec.network.mode).toBe("public");
    expect((spec as unknown as { domain: string }).domain).toBe("kombify.me");
    expect(spec.email).toBeUndefined();
    expect(spec.adminEmail).toBeUndefined();
    expect(spec.owner).toEqual({
      bootstrapMode: "auto",
      source: "cloud",
      recoveryMaterialRef: "techstack://recovery/stacks/homelab",
    });
    expect(spec.metadata.owner_bootstrap_mode).toBe("auto");
    expect(spec.metadata.owner_source).toBe("cloud");
    expect(spec.metadata.server_provisioning_mode).toBe("kombify-cloud");
    expect(spec.metadata.stackkit_foundation).toBe("cloud-kit");
    expect(spec.metadata.stackkit_catalog_ref).toBe("cloud-kit");
    expect((spec as unknown as { stackkit: string }).stackkit).toBe(
      "cloud-kit",
    );
    expect(spec.metadata.server_mode).toBe("monthly-runtime");
    expect(spec.metadata.runtime_lane).toBe("monthly-runtime");
    expect(spec.metadata.address_mode).toBe("kombify-me");
    expect(spec.metadata.provider_id).toBe("centron");
    expect(spec.metadata.billing_mode).toBe("subscription");
    expect(spec.metadata.owner_email_present).toBe("false");
    expect(spec.metadata.owner_username_present).toBe("false");
    expect(spec.metadata.recovery_passphrase_hash_present).toBe("false");
    expect(spec.metadata.recovery_material_ref_present).toBe("true");
  });

  it("keeps managed Cloud Kit on Coolify and propagates the selected IONOS provider", () => {
    const config = createDefaultConfig();
    applyServerProvisioningMode(config, "kombify-cloud");
    config.providerId = "ionos";
    config.ionosDatacenter = "us/ewr";

    const spec = buildStackKitSpecFromStackConfig(config) as {
      mode: string;
      context: string;
      domain: string;
      network: { mode: string };
      subdomainPrefix?: string;
      paas: string;
      services: Record<string, { enabled: boolean }>;
      metadata: Record<string, string>;
      owner: {
        bootstrapMode: string;
        source: string;
        recoveryMaterialRef?: string;
      };
    };

    expect(spec.context).toBe("cloud");
    expect(spec.mode).toBe("bootstrapped");
    expect((spec as unknown as { stackkit: string }).stackkit).toBe(
      "cloud-kit",
    );
    expect(spec.domain).toBe("kombify.me");
    expect(spec.network.mode).toBe("public");
    expect(spec.subdomainPrefix).toBeUndefined();
    expect(spec.paas).toBe("coolify");
    expect(spec.services.dokploy).toEqual({ enabled: false });
    expect(spec.services.coolify).toEqual({ enabled: true });
    expect(spec.metadata.runtime_lane).toBe("monthly-runtime");
    expect(spec.metadata.runtime_offering_id).toBe("monthly-runtime-standard");
    expect(spec.metadata.address_mode).toBe("kombify-me");
    expect(spec.metadata.provider_id).toBe("ionos");
    expect(spec.metadata.ionos_datacenter).toBe("us/ewr");
    expect(spec.metadata.provider_region).toBe("us/ewr");
    expect(spec.metadata).not.toHaveProperty("lease_provider");
    expect(spec.metadata).not.toHaveProperty("simulate_provider_id");
    expect(spec.metadata.billing_mode).toBe("subscription");
    expect(spec.owner.bootstrapMode).toBe("auto");
    expect(spec.owner.source).toBe("cloud");
    expect(spec.owner.recoveryMaterialRef).toBe(
      "techstack://recovery/stacks/homelab",
    );
  });

  it("does not use reserved platform names as StackSpec names", () => {
    const config = createDefaultConfig();
    config.name = "TechStack-3";

    const spec = buildStackKitSpecFromStackConfig(config) as {
      name: string;
      owner: { recoveryMaterialRef?: string };
    };

    expect(spec.name).toBe("homelab");
    expect(spec.owner.recoveryMaterialRef).toBeUndefined();
  });

  it("marks local owner metadata when bootstrap identity fields are present", () => {
    const config = createDefaultConfig();
    config.owner.source = "local";
    config.owner.email = "owner@example.com";
    config.owner.username = "owner";
    config.owner.displayName = "Owner";
    config.owner.recoveryPassphraseHash =
      "$argon2id$v=19$m=65536,t=3,p=4$demo$signed";

    const spec = buildStackKitSpecFromStackConfig(config) as {
      owner: {
        source: string;
        email?: string;
        username?: string;
        displayName?: string;
      };
      metadata: Record<string, string>;
    };

    expect(spec.metadata.owner_source).toBe("local");
    expect(spec.metadata.owner_bootstrap_mode).toBe("custom");
    expect(spec.owner).toEqual({
      bootstrapMode: "custom",
      source: "local",
      email: "owner@example.com",
      username: "owner",
      displayName: "Owner",
    });
    expect(spec.metadata.owner_email_present).toBe("true");
    expect(spec.metadata.owner_username_present).toBe("true");
    expect(spec.metadata.owner_display_name_present).toBe("true");
    expect(spec.metadata.recovery_passphrase_hash_present).toBe("true");
    expect(JSON.stringify(spec)).not.toContain("$argon2id$");
  });
});
