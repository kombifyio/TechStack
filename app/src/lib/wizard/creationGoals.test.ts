import { describe, expect, it } from "vitest";
import type { UseCaseCatalogView } from "#lib/api/useCaseCatalog.js";
import {
  supportsCreationChoice,
  withCreationTargets,
} from "./creationGoals.js";

describe("creation choices at the runtime intent boundary", () => {
  it("does not admit a planned tool or setting through display-only product targets", () => {
    const released: UseCaseCatalogView = {
      components: [
        {
          id: "minecraft-server",
          name: "Minecraft",
          role: "primary",
          kind: "application",
        },
      ],
      deliverable: false,
      settings: [
        {
          id: "backend",
          name: "Preferred service",
          kind: "choice",
          group: "backend",
          depth: "summary",
          options: [{ id: "minecraft-server", name: "Minecraft" }],
          default: "minecraft-server",
          realization: "recorded",
        },
      ],
      computeTiers: {},
    };
    const catalog = new Map([["game", released]]);
    withCreationTargets(catalog);
    expect(
      supportsCreationChoice(catalog.get("game"), "backend", "pterodactyl"),
    ).toBe(false);
    expect(
      supportsCreationChoice(catalog.get("game"), "edition", "bedrock"),
    ).toBe(false);
    expect(
      supportsCreationChoice(
        catalog.get("game"),
        "backend",
        "minecraft-server",
      ),
    ).toBe(true);
    expect(supportsCreationChoice(undefined, "backend", "pterodactyl")).toBe(
      false,
    );
  });
});
