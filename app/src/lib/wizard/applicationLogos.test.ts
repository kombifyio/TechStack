import { expect, it } from "vitest";
import type { UseCaseCatalogView } from "#lib/api/useCaseCatalog.js";
import { useCaseApplicationLogos } from "./applicationLogos.js";

it("shows an optional application only when its differently named installing setting enables it", () => {
  const home = { id: "home-assistant", name: "Home Assistant" };
  const zigbee = { id: "zigbee2mqtt", name: "Zigbee2MQTT" };
  const source: UseCaseCatalogView = {
    deliverable: true,
    computeTiers: {},
    components: [
      { ...home, role: "primary", kind: "application" },
      { ...zigbee, role: "supporting", kind: "application" },
    ],
    settings: [
      {
        id: "zigbee-bridge",
        name: "Zigbee bridge",
        kind: "toggle",
        group: "features",
        depth: "summary",
        workloadRef: "smart-home-zigbee",
        default: false,
        realization: "install",
      },
    ],
  };

  expect(useCaseApplicationLogos(source, {})).toEqual([home]);
  expect(useCaseApplicationLogos(source, { "zigbee-bridge": true })).toEqual([
    home,
    zigbee,
  ]);
  source.settings[0].realization = "recorded";
  expect(useCaseApplicationLogos(source, { "zigbee-bridge": true })).toEqual([
    home,
  ]);
});
