import type {
  UseCaseCatalogView,
  UseCaseCatalogComponent,
  UseCaseSetting,
} from "#lib/api/useCaseCatalog.js";
import snapshot from "./creationPreviewCatalog.json";
import { withCreationTargets } from "./creationGoals.js";

// Design preview only. Runtime choices always come from the released catalog.
const component = (
  id: string,
  name: string,
  role: UseCaseCatalogComponent["role"],
): UseCaseCatalogComponent => ({ id, name, role, kind: "application" });

export function createPreviewCatalog(): Map<string, UseCaseCatalogView> {
  const catalog = new Map<string, UseCaseCatalogView>(
    snapshot.useCases.map((entry) => [
      entry.id,
      {
        components: entry.components as UseCaseCatalogComponent[],
        computeTiers: entry.computeTiers,
        settings: entry.settings as UseCaseSetting[],
        releaseTag: snapshot.source.tag,
      },
    ]),
  );
  catalog.set("automation", {
    components: [
      component("n8n", "n8n", "primary"),
      component("activepieces", "Activepieces", "alternative"),
    ],
    computeTiers: {},
    settings: [],
  });
  return withCreationTargets(catalog, true);
}
