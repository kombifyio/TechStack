import type { UseCaseCatalogView } from "#lib/api/useCaseCatalog.js";
import type { StackConfig } from "./types";
import { selectedCanonicalUseCasesFromGoals } from "./standardBundle";

/**
 * Whether the selected use cases cannot run without a GPU on the pinned
 * release: a selected capability declares `requiresAccelerator`, or a chosen
 * setting option declares `needsAccelerator`. Unset answers take the catalog
 * default, as release authoring does. The server derives the same answer
 * (`UseCaseCatalog.RequiresAccelerator`) and refuses a run without a GPU; this
 * copy only lets the hypervisor step say so before anyone presses Create.
 */
export function useCasesRequireAccelerator(
  catalog: Map<string, UseCaseCatalogView>,
  config: Pick<
    StackConfig,
    "goals" | "useCaseSettings" | "useCaseCapabilities"
  >,
): boolean {
  return selectedCanonicalUseCasesFromGoals(config.goals).some((goal) => {
    const view = catalog.get(goal);
    if (!view) return false;
    const capabilities = config.useCaseCapabilities?.[goal] ?? {};
    const needsCapability = (view.capabilities ?? []).some((capability) => {
      if (!capability.requiresAccelerator) return false;
      let value = capabilities[capability.id];
      if (
        value === undefined &&
        (capability.required || capability.enabledByDefault)
      )
        value = capability.default?.id;
      return Boolean(value) && value !== "off";
    });
    if (needsCapability) return true;
    const settings = config.useCaseSettings?.[goal] ?? {};
    return (view.settings ?? []).some((setting) => {
      if (setting.kind !== "choice") return false;
      const value = settings[setting.id] ?? setting.default;
      return (setting.options ?? []).some(
        (option) => option.needsAccelerator && option.id === value,
      );
    });
  });
}
