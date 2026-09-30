import { tr } from "#lib/i18n.svelte.js";
/**
 * Dashboard presets: the layouts an owner can pick for the homelab start page.
 * Each preset is one component under `lib/components/dashboard/`; they share
 * one data load in `HomelabDashboardPage.svelte`.
 *
 * Adding a preset: add its id here, give it an entry in DASHBOARD_PRESETS and
 * a branch in `HomelabDashboardPage.svelte`.
 */
export const DASHBOARD_PRESET_IDS = ["complete", "strata"] as const;
export type DashboardPresetId = (typeof DASHBOARD_PRESET_IDS)[number];

export const DEFAULT_DASHBOARD_PRESET: DashboardPresetId = "complete";

export interface DashboardPreset {
  id: DashboardPresetId;
  label: string;
  description: string;
}

export const DASHBOARD_PRESETS: readonly DashboardPreset[] = [
  {
    id: "complete",
    get label() {
      return tr("ui.presets.complete");
    },
    get description() {
      return tr("ui.presets.nodeRowsWithTheirApps");
    },
  },
  {
    id: "strata",
    label: "Strata",
    get description() {
      return tr("ui.presets.aCalmOverviewOneBand");
    },
  },
];

export function isDashboardPresetId(
  value: unknown,
): value is DashboardPresetId {
  return (
    typeof value === "string" &&
    (DASHBOARD_PRESET_IDS as readonly string[]).includes(value)
  );
}

/**
 * What an unlock decision may look at. Empty today; usage-based unlocks
 * will add their signals here.
 */
export interface PresetAvailabilityContext {
  ownerKey: string;
}

/**
 * The unlock seam. Every preset is available now; a future usage-based unlock
 * narrows this and the Settings picker and the dashboard both follow it.
 */
export function isPresetAvailable(
  preset: DashboardPresetId,
  context: PresetAvailabilityContext,
): boolean {
  void preset;
  void context;
  return true;
}

/** The preset to render: the stored choice if it is still available. */
export function effectivePreset(
  stored: DashboardPresetId,
  context: PresetAvailabilityContext,
): DashboardPresetId {
  return isPresetAvailable(stored, context) ? stored : DEFAULT_DASHBOARD_PRESET;
}
