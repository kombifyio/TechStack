/**
 * Rail width and fold are a device preference, like the finish: they belong to
 * this browser and never to the account. One key holds `{ collapsed, width }`
 * (KOMBIFY-DESIGN-SYSTEM-STANDARD §7, 2026-10-03). The key is the one the
 * width-only format used, so a saved width survives the format change.
 */
import {
  clampRailWidth,
  DEFAULT_RAIL_BOUNDS,
  readRailPreference,
  writeRailPreference,
  type RailPreference,
} from "@kombiverselabs/ui/shell";

export const RAIL_PREFERENCE_KEY = "techstack-rail-width";
/** The earlier format kept the fold in a second key next to a bare width. */
const LEGACY_COLLAPSED_KEY = `${RAIL_PREFERENCE_KEY}-collapsed`;

/**
 * Read a bare-width value (and its separate fold key) as the shared shape and
 * rewrite it. The legacy keys are removed only after the new value is stored,
 * so a full or refusing storage never loses the saved fold.
 */
function migrateLegacyPreference(): RailPreference | null {
  try {
    const raw = localStorage.getItem(RAIL_PREFERENCE_KEY);
    if (raw === null || raw.trimStart().startsWith("{")) return null;
    const width = Number(raw);
    const legacy: RailPreference = {
      collapsed: localStorage.getItem(LEGACY_COLLAPSED_KEY) === "true",
      width:
        Number.isFinite(width) && width > 0
          ? width
          : DEFAULT_RAIL_BOUNDS.defaultWidth,
    };
    try {
      localStorage.setItem(RAIL_PREFERENCE_KEY, JSON.stringify(legacy));
      localStorage.removeItem(LEGACY_COLLAPSED_KEY);
    } catch {
      // Keep the legacy keys; this load still uses the legacy values.
    }
    return legacy;
  } catch {
    // A browser that refuses storage still gets a working rail.
    return null;
  }
}

export function loadRailPreference(): RailPreference {
  const legacy = migrateLegacyPreference();
  if (!legacy) return readRailPreference(RAIL_PREFERENCE_KEY);
  const { minWidth, maxWidth } = DEFAULT_RAIL_BOUNDS;
  return {
    collapsed: legacy.collapsed,
    width: clampRailWidth(legacy.width, minWidth, maxWidth),
  };
}

export function saveRailPreference(preference: RailPreference): void {
  writeRailPreference(RAIL_PREFERENCE_KEY, preference);
}
