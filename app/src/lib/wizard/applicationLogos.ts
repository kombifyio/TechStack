import type { ApplicationClusterItem } from "@kombiverselabs/ui/applications";
import {
  installsService,
  type UseCaseCatalogView,
} from "#lib/api/useCaseCatalog.js";
import { distinctToolLogos } from "#lib/brand-logo.js";
import { selectedBackend } from "./creationGoals.js";

// The released StackKits catalog names workload refs separately from component
// IDs. These bindings come from foundation/use_case_catalog.cue (v0.47.2);
// defaults and installation capability always come from the runtime settings.
const optionalSettingIds: Record<string, string> = {
  "immich-public-proxy": "public-sharing",
  "immich-kiosk": "kiosk",
  "immich-power-tools": "power-tools",
  zigbee2mqtt: "zigbee-bridge",
  esphome: "esphome",
  navidrome: "music-server",
  audiobookshelf: "audiobooks",
  "euro-office": "office-editing",
  "paperless-ngx": "paperless",
};

export function useCaseApplicationLogos(
  source: UseCaseCatalogView | undefined,
  values: Record<string, string | boolean>,
  reviewMode = false,
): ApplicationClusterItem[] {
  if (!source || (!reviewMode && source.deliverable === false)) return [];
  const backend = selectedBackend(source, values.backend);
  if (!backend || !installsService(backend)) return [];

  const applications = source.components
    .filter((component) => {
      if (component.kind !== "application") return false;
      if (component.role === "primary" || component.role === "alternative")
        return component.id === backend.id;

      const settingId = optionalSettingIds[component.id];
      const optional = source.settings.find((setting) =>
        settingId
          ? setting.id === settingId
          : setting.workloadRef === component.id,
      );
      // A known add-on without installing settings is not part of the package.
      if (settingId && !optional) return false;
      return (
        !optional ||
        (optional.realization === "install" &&
          (values[optional.id] ?? optional.default) === true)
      );
    })
    .map(({ id, name }) => ({ id, name }));

  // Add-ons can share their application's mark. The detailed tool list still
  // names every component; the illustration retains the selected service.
  return distinctToolLogos(applications, backend.id);
}
