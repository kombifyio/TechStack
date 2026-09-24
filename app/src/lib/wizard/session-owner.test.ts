import { describe, expect, it } from "vitest";

import { createDefaultConfig } from "./types";
import {
  ownerUsernameFromEmail,
  seedLocalOwnerFromSignedInAccount,
} from "./session-owner";

describe("signed-in owner default", () => {
  it("fills an empty local owner from the signed-in account", () => {
    const config = createDefaultConfig();
    seedLocalOwnerFromSignedInAccount(config.owner, {
      email: "Cloud.Owner+Lab@example.com",
      displayName: "Cloud Owner",
    });

    expect(config.owner.email).toBe("Cloud.Owner+Lab@example.com");
    expect(config.owner.username).toBe("cloud-owner-lab");
    expect(config.owner.displayName).toBe("Cloud Owner");
  });

  it("keeps an explicit local owner email", () => {
    const config = createDefaultConfig();
    config.owner.email = "other.owner@example.com";
    config.owner.username = "other";
    seedLocalOwnerFromSignedInAccount(config.owner, {
      email: "cloud.owner@example.com",
      displayName: "Cloud Owner",
    });

    expect(config.owner.email).toBe("other.owner@example.com");
    expect(config.owner.username).toBe("other");
    expect(config.owner.displayName).toBe("");
  });

  it("does not seed a cloud-linked owner", () => {
    const config = createDefaultConfig();
    config.owner.source = "cloud-linked";
    seedLocalOwnerFromSignedInAccount(config.owner, {
      email: "cloud.owner@example.com",
      displayName: "Cloud Owner",
    });

    expect(config.owner.email).toBe("");
    expect(config.owner.username).toBe("");
    expect(ownerUsernameFromEmail("Cloud.Owner+Lab@example.com")).toBe(
      "cloud-owner-lab",
    );
  });
});
