import { isAppIconSlug, type AppLogoOverride } from "#lib/brand-logo.js";

/*
 * Per-viewer logo choice for one application, kept in this browser only
 * (localStorage): "hidden" draws the monogram, "icon" draws another
 * dashboard-icons slug from our asset host. It works without storage too: a
 * blocked or full storage keeps the choice for this page view only.
 */
const STORAGE_KEY = "techstack.appLogoOverrides.v1";

/** One application, whatever the casing the control plane reports. */
export function appLogoKey(
  ...candidates: Array<string | undefined | null>
): string {
  for (const candidate of candidates) {
    const key = (candidate || "").trim().toLowerCase();
    if (key) return key;
  }
  return "";
}

function readOverride(value: unknown): AppLogoOverride | undefined {
  if (!value || typeof value !== "object") return undefined;
  const record = value as Record<string, unknown>;
  if (record.mode === "hidden") return { mode: "hidden" };
  if (record.mode === "icon" && isAppIconSlug(record.slug))
    return { mode: "icon", slug: record.slug };
  return undefined;
}

function load(): Record<string, AppLogoOverride> {
  const stored: Record<string, AppLogoOverride> = {};
  try {
    const parsed: unknown = JSON.parse(
      localStorage.getItem(STORAGE_KEY) || "{}",
    );
    if (parsed && typeof parsed === "object") {
      for (const [key, value] of Object.entries(parsed)) {
        const override = readOverride(value);
        if (key && override) stored[key] = override;
      }
    }
  } catch {
    // No storage, or a damaged value: every application is automatic.
  }
  return stored;
}

let overrides = $state<Record<string, AppLogoOverride>>(load());

/** This viewer's override for an application; reactive. */
export function getAppLogoOverride(
  key: string | undefined | null,
): AppLogoOverride | undefined {
  return key ? overrides[key] : undefined;
}

/** Hide, replace or (with undefined) reset the logo of one application. */
export function setAppLogoOverride(
  key: string,
  override: AppLogoOverride | undefined,
): void {
  if (!key) return;
  const next = { ...overrides };
  if (override) next[key] = override;
  else delete next[key];
  overrides = next;
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
  } catch {
    // Storage is a convenience; the choice still holds for this page view.
  }
}
