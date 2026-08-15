import { afterEach, describe, expect, it, vi } from "vitest";

import { ManagementStateContractError } from "./api/registry";
import {
  openServiceUrl,
  requireServiceManagementState,
  serviceCardMeta,
  serviceCardMetrics,
  serviceCardName,
  serviceCardPlacement,
  serviceCardStatus,
  serviceCardStatusMessage,
  serviceHasActiveMigration,
  serviceManagementState,
  serviceTargetLabel,
  serviceTypeLabel,
  type TechStackServiceCardSource,
} from "./service-card-adapter";

describe("service card adapter", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reports a missing ownership dimension as unknown, never as not-managed", () => {
    // The regression this pins: `management_state` used to be read as
    // `(service.management_state || "").toLowerCase()`, so a response that
    // dropped the field compared unequal to "managed" and every service
    // silently lost its managed-only controls.
    expect(serviceManagementState({})).toBe("unknown");
    expect(serviceManagementState({ management_state: "" })).toBe("unknown");
    expect(serviceManagementState({ management_state: "supervised" })).toBe(
      "unknown",
    );
    expect(serviceManagementState({ management_state: " Managed " })).toBe(
      "managed",
    );
    expect(serviceManagementState({ management_state: "OBSERVED" })).toBe(
      "observed",
    );
  });

  it("refuses to guess ownership where the read model guarantees it", () => {
    expect(() => requireServiceManagementState({})).toThrow(
      ManagementStateContractError,
    );
    expect(() =>
      requireServiceManagementState({ management_state: "supervised" }),
    ).toThrow(ManagementStateContractError);
    expect(requireServiceManagementState({ management_state: "managed" })).toBe(
      "managed",
    );
  });

  it("keeps ownership separate from runtime card status", () => {
    // An observed/unmanaged service can be healthy.  It must never be shown
    // as Frozen merely because ownership is not managed.
    expect(
      serviceCardStatus({ management_state: "observed", status: "running" }),
    ).toBe("running");
    expect(serviceCardStatus({ status: "running" })).toBe("running");
    expect(serviceCardStatusMessage({ status: "running" })).toBeUndefined();
  });

  it("requires explicit migration evidence instead of normal running status", () => {
    expect(serviceHasActiveMigration({ status: "running" })).toBe(false);
    expect(
      serviceHasActiveMigration({
        status: "running",
        migration_status: "running",
      }),
    ).toBe(false);
    expect(
      serviceHasActiveMigration({
        status: "running",
        migration_status: "migrating",
      }),
    ).toBe(false);
    expect(
      serviceHasActiveMigration({
        status: "running",
        operation_state: "running",
      }),
    ).toBe(true);
  });

  it("builds stable names, labels, metadata, and metrics from backend service fields", () => {
    const service: TechStackServiceCardSource = {
      name: "homepage",
      display_name: "Homepage",
      type: "media_server",
      target_server: "Foundation",
      node_name: "Worker",
      port: 8080,
    };

    expect(serviceCardName(service)).toBe("Homepage");
    expect(serviceCardName({ name: "postgres" })).toBe("postgres");
    expect(serviceCardName({})).toBe("Service");
    expect(serviceTypeLabel(service)).toBe("media server");
    expect(serviceTargetLabel(service)).toBe("Foundation");
    expect(serviceCardMeta(service)).toBe("media server · Foundation · :8080");
    expect(serviceCardMetrics(service)).toEqual([
      { label: "Type", value: "media server" },
      { label: "Server", value: "Foundation" },
      { label: "Port", value: "8080" },
    ]);
  });

  it("uses all backend target fallbacks before rendering a service without a server metric", () => {
    expect(serviceTargetLabel({ node_name: "Node A" })).toBe("Node A");
    expect(serviceTargetLabel({ server_name: "Server B" })).toBe("Server B");
    expect(serviceTargetLabel({ target_server_id: "target-1" })).toBe(
      "target-1",
    );
    expect(serviceTargetLabel({ node_id: "node-1" })).toBe("node-1");
    expect(serviceTargetLabel({ server_id: "server-1" })).toBe("server-1");
    expect(serviceTargetLabel({})).toBeUndefined();
    expect(serviceCardMetrics({ type: "cache" })).toEqual([
      { label: "Type", value: "cache" },
    ]);
  });

  it("classifies placement from service fields and route-specific hints", () => {
    expect(serviceCardPlacement({ type: "serverless_function" })).toBe(
      "serverless",
    );
    expect(
      serviceCardPlacement({ node_name: "kombify managed runtime lease" }),
    ).toBe("cloud");
    expect(serviceCardPlacement({ type: "database" }, "managed-runtime")).toBe(
      "cloud",
    );
    expect(
      serviceCardPlacement({ type: "database", server_name: "local-1" }),
    ).toBe("local");
    expect(serviceCardPlacement({ type: "database" })).toBe("unknown");
    expect(
      serviceCardPlacement({ type: "database", provider: "Hostinger VPS" }),
    ).toBe("cloud");
    expect(serviceCardPlacement({ type: "database", placement: "cloud" })).toBe(
      "cloud",
    );
  });

  it("maps backend lifecycle state into service card statuses", () => {
    const cases: Array<{
      service: TechStackServiceCardSource;
      expected: ReturnType<typeof serviceCardStatus>;
    }> = [
      {
        service: { management_state: "observed", status: "running" },
        expected: "running",
      },
      { service: { status: "archived" }, expected: "frozen" },
      { service: { status: "update_available" }, expected: "update" },
      { service: { status: "pending_verification" }, expected: "running" },
      { service: { status: "adopting" }, expected: "pending" },
      { service: { status: "degraded" }, expected: "error" },
      { service: { status: "unhealthy" }, expected: "error" },
      { service: { status: "starting" }, expected: "pending" },
      { service: { status: "unknown" }, expected: "unknown" },
      { service: { status: "reachable" }, expected: "running" },
      { service: { status: "offline" }, expected: "stopped" },
      { service: { status: "RUNNING" }, expected: "running" },
      { service: {}, expected: "unknown" },
      {
        service: { status: "running", migration_status: "running" },
        expected: "running",
      },
      {
        service: { status: "running", operation_state: "running" },
        expected: "migrating",
      },
    ];

    for (const { service, expected } of cases) {
      expect(serviceCardStatus(service)).toBe(expected);
    }
  });

  it("returns operator-facing status messages for non-happy paths", () => {
    expect(
      serviceCardStatusMessage({
        management_state: "observed",
        status: "running",
      }),
    ).toBeUndefined();
    expect(serviceCardStatusMessage({ status: "archived" })).toBe(
      "Archived source service.",
    );
    expect(
      serviceCardStatusMessage({
        status: "failed",
        move_blocked_reason: "Needs a compatible target server.",
      }),
    ).toBe("Needs a compatible target server.");
    expect(serviceCardStatusMessage({ status: "error" })).toBe(
      "Service reported an error.",
    );
    expect(serviceCardStatusMessage({ status: "pending" })).toBe(
      "Waiting for placement.",
    );
    expect(serviceCardStatusMessage({ status: "starting" })).toBe(
      "Waiting for a Docker health or endpoint probe.",
    );
    expect(serviceCardStatusMessage({ status: "unknown" })).toBe(
      "No current runtime observation is available.",
    );
    expect(serviceCardStatusMessage({ status: "reachable" })).toBe(
      "Endpoint is reachable, but protected service health is not verified.",
    );
    expect(serviceCardStatusMessage({ status: "running" })).toBeUndefined();
  });

  it("opens only services that expose an external URL", () => {
    const open = vi.fn();
    vi.stubGlobal("window", { open });

    openServiceUrl({});
    expect(open).not.toHaveBeenCalled();

    openServiceUrl({ url: "https://service.example.test" });
    expect(open).toHaveBeenCalledWith(
      "https://service.example.test",
      "_blank",
      "noopener,noreferrer",
    );
  });
});
