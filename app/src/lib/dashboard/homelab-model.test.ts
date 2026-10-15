import { describe, expect, it } from "vitest";

import type { CanonicalService } from "#lib/api/services.js";
import type { HomelabIdentityStatus } from "#lib/api/homelabIdentity.js";
import type { ConnectDevice } from "#lib/api/connectDevices.js";
import {
  applicationOpenUrl,
  nodeApps,
  oldestFirst,
  serviceState,
  stackedAppsOnly,
} from "./homelab-model";
import { devicesFromConnect, peopleFromIdentity } from "./people";

function service(
  id: string,
  serverId: string | undefined,
  key: string,
): CanonicalService {
  return {
    id,
    kit_deployment_id: "dep-1",
    server_id: serverId,
    target_kind: "server",
    placement: {} as CanonicalService["placement"],
    service_key: key,
    service_instance: key,
    name: key,
    management_state: "managed",
    desired_state: "running",
    observed_state: "running",
    health: { state: "healthy" } as CanonicalService["health"],
    access: {},
    allowed_actions: [],
    inventory_revision: 1,
    source: "stackkit",
    provenance: {},
    created_at: "",
    updated_at: "",
  };
}

describe("nodeApps", () => {
  it("shows one tile per application with a UI and leaves the rest to the server", () => {
    const app = (
      id: string,
      key: string,
      extra: Partial<CanonicalService> = {},
    ): CanonicalService => ({
      ...service(id, "node-1", key),
      application_key: key,
      ...extra,
    });
    const services = [
      // Hermes: web UI with an address plus its backend, one application.
      app("hermes-ui", "hermes", {
        access: { url: "https://hermes.home.example" },
      }),
      app("hermes-api", "hermes"),
      app("pocket", "pocket-id", {
        catalog: {
          id: "pocket-id",
          display_name: "Pocket ID",
          category: "security",
        },
      }),
      app("proxy", "traefik"),
      app("db", "kombify-db-local", {
        catalog: {
          id: "postgres",
          display_name: "PostgreSQL",
          category: "database",
        },
      }),
      app("elsewhere", "jellyfin", { server_id: "node-2" }),
    ];
    const result = nodeApps(
      services,
      new Set(["node-1"]),
      new Set(["pocket-id"]),
    );
    expect(result.apps.map((item) => item.id)).toEqual(["pocket", "hermes-ui"]);
    expect(result.members["hermes-ui"]?.map((item) => item.id)).toEqual([
      "hermes-ui",
      "hermes-api",
    ]);
    expect(applicationOpenUrl(result.members["hermes-ui"] ?? [])).toBe(
      "https://hermes.home.example",
    );
    expect(result.system.map((item) => item.id).sort()).toEqual([
      "db",
      "proxy",
    ]);
  });
});

describe("stackedAppsOnly", () => {
  it("shows found unstacked apps until the StackKit is rolled out, then only stacked ones", () => {
    const app = (
      id: string,
      management: CanonicalService["management_state"],
    ): CanonicalService => ({
      ...service(id, "node-1", id),
      application_key: id,
      management_state: management,
      access: { url: `https://${id}.home.example` },
    });
    const found = nodeApps(
      [app("immich", "managed"), app("hermes", "observed")],
      new Set(["node-1"]),
      new Set(),
    );
    const ids = (manifest: boolean) =>
      stackedAppsOnly(found, manifest).apps.map((item) => item.id);

    expect(ids(false)).toEqual(["hermes", "immich"]);
    expect(ids(true)).toEqual(["immich"]);
    expect(
      stackedAppsOnly(found, true).system.map((item) => item.id),
    ).toContain("hermes");
  });
});

describe("oldestFirst", () => {
  it("lists a newly joined Node after the running ones", () => {
    const nodes = [
      { key: "new", addedAt: "2026-10-07T10:00:00Z" },
      { key: "undated", addedAt: "" },
      { key: "first", addedAt: "2026-09-01T10:00:00Z" },
    ];
    expect(oldestFirst(nodes).map((node) => node.key)).toEqual([
      "first",
      "new",
      "undated",
    ]);
  });
});

describe("peopleFromIdentity", () => {
  it("never turns a failed household read into an empty household", () => {
    const failedSnapshot: HomelabIdentityStatus = {
      owner: { status: "failed", reason_code: "identity_status_failed" },
      household: { members: [] },
    };
    expect(
      peopleFromIdentity(
        [{ status: "fulfilled", value: failedSnapshot }],
        "Owner",
      ).state,
    ).toBe("unavailable");
    expect(
      peopleFromIdentity(
        [{ status: "rejected", reason: new Error("502") }],
        "Owner",
      ).state,
    ).toBe("unavailable");
  });
});

describe("serviceState", () => {
  it("reads an unmeasured health as absent instead of labelling the app unknown", () => {
    const probed = service("a", "node-1", "hermes-webui");
    probed.health = { state: "unknown" };
    expect(serviceState(probed)).toEqual({ label: "running", tone: "ok" });
    probed.observed_state = "unknown";
    expect(serviceState(probed).label).toBe("");
  });
});

describe("devicesFromConnect", () => {
  it("folds installations of one machine without a device_ref into one device", () => {
    const install = (id: string, appId: string, seen: string) => ({
      installation_id: id,
      app_id: appId,
      platform: "windows",
      display_name: appId,
      app_version: "1.0.0",
      status: "active",
      created_at: seen,
      last_seen_at: seen,
      environments: [],
    });
    const device = (
      id: string,
      grouping: string,
      name: string,
      installations: ConnectDevice["installations"],
    ): ConnectDevice => ({
      device_id: id,
      grouping,
      display_name: name,
      platform: "windows",
      last_seen_at: installations[0]?.last_seen_at,
      installations,
    });
    const result = devicesFromConnect({
      state: "ready",
      devices: [
        device("i1", "installation", "DeskAlex", [
          install("i1", "workbench-desktop", "2026-09-26T08:00:00Z"),
        ]),
        device("i2", "installation", "deskalex ", [
          install("i2", "workbench-desktop", "2026-09-26T09:00:00Z"),
        ]),
        device("ref-1", "device_ref", "DeskAlex", [
          install("c1", "companion", "2026-09-26T07:00:00Z"),
        ]),
        device("ref-2", "device_ref", "Laptop", [
          install("c2", "companion", "2026-09-26T07:00:00Z"),
        ]),
      ],
    });
    if (result.state !== "ready") throw new Error(result.state);
    const desk = result.items.find((item) => item.name === "DeskAlex");
    expect(result.items.map((item) => item.name).sort()).toEqual([
      "DeskAlex",
      "Laptop",
    ]);
    expect(desk?.clients.sort()).toEqual(["Companion", "Workbench ×2"]);
    expect(desk?.lastSeen).toBe("2026-09-26T09:00:00Z");
  });
});
