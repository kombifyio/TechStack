import { describe, expect, it } from "vitest";
import { getRequiredServices, getRecommendedServices } from "./services";

describe("basement-kit verified service catalog", () => {
  it("uses the StackKits verified StackKit service set", () => {
    const required = getRequiredServices("basement-kit").map(
      (service) => service.id,
    );
    const recommended = getRecommendedServices("basement-kit").map(
      (service) => service.id,
    );

    expect(required).toEqual(expect.arrayContaining(["traefik", "pocket-id"]));
    expect(recommended).toEqual(
      expect.arrayContaining([
        "vaultwarden",
        "immich-server",
        "immich-ml",
        "immich-postgres",
        "immich-redis",
      ]),
    );
    expect(recommended).not.toContain("files");
  });
});
