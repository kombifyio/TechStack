import { describe, expect, it } from "vitest";
import type { UseCaseCatalogView } from "#lib/api/useCaseCatalog.js";
import {
  backendChoices,
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

it("offers the default app first although the release sorts components by id", () => {
  const photos: UseCaseCatalogView = {
    components: [
      {
        id: "ente-photos",
        name: "Ente Photos",
        role: "alternative",
        kind: "application",
      },
      { id: "immich", name: "Immich", role: "primary", kind: "application" },
      {
        id: "immich-kiosk",
        name: "Immich Kiosk",
        role: "supporting",
        kind: "application",
      },
      {
        id: "immich-lite",
        name: "Immich Lite",
        role: "alternative",
        kind: "application",
      },
    ],
    computeTiers: {},
    settings: [],
  };
  expect(backendChoices(photos).map((component) => component.id)).toEqual([
    "immich",
    "ente-photos",
    "immich-lite",
  ]);
});
