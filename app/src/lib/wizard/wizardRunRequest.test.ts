import { describe, expect, it } from "vitest";
import { applyServerProvisioningMode, createDefaultConfig } from "./types";
import { CANONICAL_USE_CASE_GOALS } from "./standardBundle";
import {
  buildFoundRunRequest,
  buildJoinRunRequest,
  buildRunOwnerOptions,
  wizardRunRequestCarriesSecret,
} from "./wizardRunRequest";
import { WIZARD_INTENT_SCHEMA } from "#lib/api/wizardRuns.js";

it("provisions a separate appliance only for an explicitly new HAOS installation", () => {
  const config = createDefaultConfig();
  applyServerProvisioningMode(config, "hypervisor");
  config.goals!["smart-home"] = true;
  config.useCaseSettings = {
    "smart-home": { "operating-form": "haos", "instance-origin": "new" },
  };
  Object.assign(config.serverProvisioning.substrate!, {
    serverId: "substrate-1",
    storage: "local",
    bridge: "vmbr0",
  });
  expect(() => buildFoundRunRequest(config)).toThrow();
  config.serverProvisioning.substrate!.appliance = {
    profileId: "haos",
    storage: "appliances",
    bridge: "vmbr1",
    cpu: 2,
    memoryMiB: 4096,
    diskGiB: 32,
  };
  expect(buildFoundRunRequest(config).substrate?.appliance).toMatchObject({
    profile_id: "haos",
    storage: "appliances",
    bridge: "vmbr1",
  });
  config.useCaseSettings["smart-home"]["instance-origin"] = "existing";
  expect(buildFoundRunRequest(config).substrate?.appliance).toBeUndefined();
});

it("carries the same bounded Ubuntu guest intent for found and join without SSH credentials", () => {
  const config = createDefaultConfig();
  applyServerProvisioningMode(config, "hypervisor");
  config.serverProvisioning.remote.host = "private-ssh-host";
  Object.assign(config.serverProvisioning.substrate!, {
    serverId: "substrate-1",
    storage: "local",
    bridge: "vmbr0",
  });
  const found = buildFoundRunRequest(config);
  const joined = buildJoinRunRequest(config, "deployment-1", "Home", []);
  expect(found.intent.server.transport).toBe("hypervisor");
  expect(joined.substrate).toEqual(found.substrate);
  expect(found.substrate).toMatchObject({
    server_id: "substrate-1",
    profile_id: "ubuntu-24.04",
    storage: "local",
    bridge: "vmbr0",
  });
  expect(found.remote).toBeUndefined();
  expect(joined.remote).toBeUndefined();
  expect(joined.owner).toBeUndefined();
  expect(JSON.stringify(found)).not.toContain("private-ssh-host");
  config.serverProvisioning.substrate!.profileId = "haos" as "ubuntu-24.04";
  expect(() => buildFoundRunRequest(config)).toThrow();
  config.serverProvisioning.substrate!.profileId = "ubuntu-24.04";
  config.serverProvisioning.substrate!.cpu = Number.NaN;
  expect(() =>
    buildJoinRunRequest(config, "deployment-1", "Home", []),
  ).toThrow();
});

describe("buildFoundRunRequest", () => {
  it("preserves access and household decisions on first run without changing an existing Homelab on join", () => {
    const config = createDefaultConfig();
    config.network.accessMode = "anywhere";
    config.network.domainBase = " Home.Example.com ";
    config.household = {
      profile: "shared",
      people: [
        { id: "person-one", name: " Alex ", email: " alex@example.com " },
        { id: "empty-row", name: "", email: "" },
      ],
    };
    const found = buildFoundRunRequest(config);
    expect(found.intent.access).toEqual({ mode: "remote-private" });
    expect(found.intent.domain_base).toBe("home.example.com");
    expect(found.intent.household).toEqual({
      profile: "shared",
      planned_people: [
        { client_ref: "person-one", name: "Alex", email: "alex@example.com" },
      ],
    });
    const joined = buildJoinRunRequest(
      config,
      "existing-deployment",
      "Home",
      [],
    );
    expect(joined.intent.access).toBeUndefined();
    expect(joined.intent.household).toBeUndefined();
    config.household.profile = "solo";
    expect(
      buildFoundRunRequest(config).intent.household?.planned_people,
    ).toBeUndefined();
  });

  it("lets the server resolve the verified account instead of forwarding stale custom owner fields", () => {
    const config = createDefaultConfig();
    config.owner.bootstrapMode = "auto";
    config.owner.source = "cloud";
    config.owner.email = "stale@example.com";
    config.owner.username = "stale";
    config.owner.displayName = "Stale draft";
    config.owner.recoveryPassphraseHash = "stale-hash";
    expect(buildFoundRunRequest(config).owner).toEqual({
      owner_bootstrap_mode: "auto",
      owner_source: "cloud",
    });
  });

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

  it("maps connect-remote password auth for enrollment", () => {
    const config = createDefaultConfig();
    config.serverProvisioning.mode = "connect-remote";
    config.serverProvisioning.remote.host = "10.0.0.5";
    config.serverProvisioning.remote.sshUser = "root";
    config.serverProvisioning.remote.authMethod = "password";
    config.serverProvisioning.remote.sshPassword = "secret";

    const request = buildFoundRunRequest(config);

    expect(request.remote).toEqual({
      host: "10.0.0.5",
      port: 22,
      user: "root",
      auth_method: "password",
      password: "secret",
    });
    expect(wizardRunRequestCarriesSecret(request)).toBe(true);
  });

  it("does not flag key-auth or managed requests as carrying a secret", () => {
    const config = createDefaultConfig();
    config.serverProvisioning.mode = "connect-remote";
    config.serverProvisioning.remote.host = "10.0.0.5";
    config.serverProvisioning.remote.sshUser = "ubuntu";
    config.serverProvisioning.remote.authMethod = "ssh-key";

    const request = buildFoundRunRequest(config);

    expect(request.remote?.password).toBeUndefined();
    expect(wizardRunRequestCarriesSecret(request)).toBe(false);
    expect(wizardRunRequestCarriesSecret({ intent: request.intent })).toBe(
      false,
    );
  });
});

describe("use-case settings on the intent", () => {
  // Closed contract: a decision travels only with the goal it belongs to, and
  // nothing travels for a use case the operator did not select. The backend
  // validates ids and values against the StackKits catalog.
  it("carries decisions for selected goals only", () => {
    const config = createDefaultConfig();
    config.goals!.photos = true;
    config.goals!.media = false;
    config.useCaseSettings = {
      photos: { "machine-learning": false, "library-volume": "dedicated-disk" },
      media: { "hardware-transcoding": "on" },
    };
    const request = buildFoundRunRequest(config);
    expect(request.intent.use_case_settings).toEqual({
      photos: { "machine-learning": false, "library-volume": "dedicated-disk" },
    });
  });

  it("sends nothing when no decision was made", () => {
    const config = createDefaultConfig();
    config.goals!.photos = true;
    expect(
      buildFoundRunRequest(config).intent.use_case_settings,
    ).toBeUndefined();
  });
});

describe("buildJoinRunRequest", () => {
  // One kit deployment has one control plane: a join that asks for a
  // foundation role is recorded as a worker. A second main Node is a second
  // kit deployment (kit_assignment mode "found"), not a join.
  it("maps Additional Node foundation role onto worker", () => {
    const request = buildJoinRunRequest(
      createDefaultConfig(),
      "stack-1",
      "my-homelab",
      [],
    );
    expect(request.intent.server.roles).toEqual(["worker"]);
  });

  // The backend folds an expansion's goals into the homelab's durable
  // backlog, so what the operator picks on an Additional Node is added, not
  // substituted. An empty pick must stay empty - that is what keeps a
  // capacity-only Node from importing use cases nobody asked for.
  it("carries the use cases picked for this Node, and nothing else", () => {
    const none = createDefaultConfig();
    for (const goal of CANONICAL_USE_CASE_GOALS) none.goals![goal] = false;
    none.goals!.everything = false;
    expect(
      buildJoinRunRequest(none, "stack-1", "my-homelab", []).intent.goals,
    ).toEqual([]);

    const picked = createDefaultConfig();
    for (const goal of CANONICAL_USE_CASE_GOALS) picked.goals![goal] = false;
    picked.goals!.everything = false;
    picked.goals!.photos = true;
    expect(
      buildJoinRunRequest(picked, "stack-1", "my-homelab", []).intent.goals,
    ).toEqual(["photos"]);
  });

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
      kit_slug: "basement-kit",
    });
    expect(request.intent.server.roles).toEqual(["worker"]);
    expect(request.owner).toBeUndefined();
    // Joins keep the StackConfig service keys the legacy pairing lane sent.
    expect(request.services).toEqual(["monitoring"]);
  });

  // Provider control is a sensitive boundary: a managed join must carry the
  // exact provider selection into the server-owned Wizard dispatch.
  it("carries managed provider admission parameters on a Cloud join", () => {
    const config = createDefaultConfig();
    applyServerProvisioningMode(config, "kombify-cloud");
    config.serverProvisioning.nodeRole = "storage";
    config.providerId = "ionos";
    config.runtimeOfferingId = "monthly-runtime-premium";
    config.ionosDatacenter = "de/fra";

    const request = buildJoinRunRequest(config, "stack-1", "my-homelab", [
      "backup",
    ]);

    expect(request.managed).toEqual({
      provider_id: "ionos",
      runtime_offering_id: "monthly-runtime-premium",
      provider_region: "de/fra",
      ionos_datacenter: "de/fra",
    });
    expect(request.intent.server).toEqual({
      transport: "kombify-cloud",
      roles: ["storage"],
    });
    expect(request.intent.kit_assignment.kit_slug).toBe("cloud-kit");
  });
});
