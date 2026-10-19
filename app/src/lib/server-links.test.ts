import { describe, expect, it } from "vitest";
import {
  addServerHref,
  serverDetailHref,
  serverHistoryHref,
} from "./server-links";

describe("server detail route", () => {
  it("is the deployment server page whenever the deployment is known", () => {
    expect(serverDetailHref({ serverId: "srv 1", deploymentId: "dep-1" })).toBe(
      "/stacks/dep-1/servers/srv%201",
    );
  });

  it("falls back to the monitoring detail only for a canonical id", () => {
    expect(serverDetailHref({ serverId: "srv-1" })).toBe("/monitoring/srv-1");
    expect(serverDetailHref({ serverId: "lease:abc" })).toBeUndefined();
    expect(serverDetailHref({ deploymentId: "dep-1" })).toBeUndefined();
  });

  it("keeps the history view one link away", () => {
    expect(serverHistoryHref("srv-1")).toBe("/monitoring/srv-1");
  });

  it("adds a server to the existing homelab, or starts creation", () => {
    expect(addServerHref("dep-1")).toBe("/stacks/dep-1/servers/new");
    expect(addServerHref()).toBe("/stacks/new");
  });
});
