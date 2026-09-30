import { describe, expect, it } from "vitest";

import type { CanonicalService } from "#lib/api/services.js";
import type { HomelabIdentityStatus } from "#lib/api/homelabIdentity.js";
import type { ConnectDevice } from "#lib/api/connectDevices.js";
import { nodeApps, serviceState } from "./homelab-model";
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
  it("places services by server_id, folds platform services and puts selected use cases first", () => {
    const services = [
      service("a", "node-1", "vaultwarden"),
      service("b", "node-1", "traefik"),
      service("c", "node-1", "immich"),
      service("d", "node-2", "jellyfin"),
      service("e", undefined, "nextcloud"),
    ];
    const result = nodeApps(services, new Set(["node-1"]), new Set(["immich"]));
    expect(result.apps.map((item) => item.id)).toEqual(["c", "a"]);
    expect(result.system.map((item) => item.id)).toEqual(["b"]);
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
        device("i1", "installation", "DeskMarcel", [
          install("i1", "workbench-desktop", "2026-09-26T08:00:00Z"),
        ]),
        device("i2", "installation", "deskmarcel ", [
          install("i2", "workbench-desktop", "2026-09-26T09:00:00Z"),
        ]),
        device("ref-1", "device_ref", "DeskMarcel", [
          install("c1", "companion", "2026-09-26T07:00:00Z"),
        ]),
        device("ref-2", "device_ref", "Laptop", [
          install("c2", "companion", "2026-09-26T07:00:00Z"),
        ]),
      ],
    });
    if (result.state !== "ready") throw new Error(result.state);
    const desk = result.items.find((item) => item.name === "DeskMarcel");
    expect(result.items.map((item) => item.name).sort()).toEqual([
      "DeskMarcel",
      "Laptop",
    ]);
    expect(desk?.clients.sort()).toEqual(["Companion", "Workbench ×2"]);
    expect(desk?.lastSeen).toBe("2026-09-26T09:00:00Z");
  });
});
