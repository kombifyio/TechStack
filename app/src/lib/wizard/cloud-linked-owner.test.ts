import { describe, expect, it } from "vitest";

import { buildPayload } from "./payload";
import { buildStackKitSpecFromStackConfig } from "./spec";
import { applyServerProvisioningMode, createDefaultConfig } from "./types";

function cloudLinkedConfig() {
  const config = createDefaultConfig();
  config.owner.bootstrapMode = "custom";
  config.owner.source = "cloud-linked";
  // Stale local fields must never reach the wire for cloud-linked owners:
  // the backend rejects client-supplied identity fields fail-closed.
  config.owner.email = "stale@example.com";
  config.owner.username = "stale";
  config.owner.displayName = "Stale";
  return config;
}

describe("cloud-linked owner wire contract", () => {
  it("buildPayload omits owner identity fields for cloud-linked owners", () => {
    const payload = buildPayload(cloudLinkedConfig());

    expect(payload.options.owner_source).toBe("cloud-linked");
    expect(payload.options.owner_email).toBeUndefined();
    expect(payload.options.owner_username).toBeUndefined();
    expect(payload.options.owner_display_name).toBeUndefined();
  });

  it("buildPayload keeps owner identity fields for local owners", () => {
    const config = createDefaultConfig();
    config.owner.bootstrapMode = "custom";
    config.owner.source = "local";
    config.owner.email = "owner@example.com";

    const payload = buildPayload(config);
    expect(payload.options.owner_email).toBe("owner@example.com");
  });

  it("stack spec omits owner identity fields for cloud-linked owners", () => {
    const spec = buildStackKitSpecFromStackConfig(cloudLinkedConfig());

    expect(spec.owner.source).toBe("cloud-linked");
    expect(spec.owner.email).toBeUndefined();
    expect(spec.owner.username).toBeUndefined();
    expect(spec.owner.displayName).toBeUndefined();
  });

  it("leaving the managed lane resets the auto cloud owner but keeps cloud-linked", () => {
    const autoCloud = createDefaultConfig();
    applyServerProvisioningMode(autoCloud, "kombify-cloud");
    expect(autoCloud.owner.source).toBe("cloud");
    applyServerProvisioningMode(autoCloud, "install-command");
    expect(autoCloud.owner.source).toBe("local");
    expect(autoCloud.owner.bootstrapMode).toBe("custom");

    const linked = cloudLinkedConfig();
    applyServerProvisioningMode(linked, "install-command");
    expect(linked.owner.source).toBe("cloud-linked");
  });
});
