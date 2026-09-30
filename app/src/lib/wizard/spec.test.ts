import { describe, expect, it } from "vitest";
import { buildStackKitSpecFromStackConfig } from "./spec";
import { applyServerProvisioningMode, createDefaultConfig } from "./types";

describe("wizard StackKit spec", () => {
  it("omits the VPN overlay for home-only access", () => {
    const config = createDefaultConfig();
    config.network.accessMode = "home";

    expect(buildStackKitSpecFromStackConfig(config).vpn).toBeUndefined();
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

  // The Techie Wizard used to be the strongest capability signal. With one
  // wizard left, depth is observed from what the operator actually did:
  // advanced interactions and explicit alternative picks. Asserted as a lift
  // over the untouched baseline rather than as a fixed score, because the
  // scale is tuned deliberately.
  it("raises capability metadata for advanced interactions and explicit alternatives", () => {
    const readScore = (config: ReturnType<typeof createDefaultConfig>) =>
      Number(
        (
          buildStackKitSpecFromStackConfig(config) as {
            metadata: Record<string, string>;
          }
        ).metadata.operator_capability_score,
      );

    const baseline = createDefaultConfig();
    const engaged = createDefaultConfig();
    engaged.serverProvisioning.stackkitFoundation = "cloud-kit";
    engaged.serverProvisioning.nodeRole = "worker";
    engaged.advanced.backupsEnabled = false;

    expect(readScore(engaged)).toBeGreaterThan(readScore(baseline));

    const spec = buildStackKitSpecFromStackConfig(engaged) as {
      metadata: Record<string, string>;
    };
    expect(spec.metadata.operator_capability_evidence).toContain("advanced:1");
    expect(spec.metadata.operator_capability_evidence).toContain(
      "alternatives:2",
    );
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
