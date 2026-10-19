// Techstack settings tabs on KOMBIFY-DESIGN-SYSTEM-STANDARD.md §10.2.1: shared tabs in the fixed
// order account, identity, design, notifications; the tool tab (homelab)
// follows. The slug is the deep-link contract (`/settings?tab=<slug>`).

export const SHARED_SETTINGS_TABS = [
  "account",
  "identity",
  "design",
  "notifications",
] as const;
export const TOOL_SETTINGS_TABS = ["homelab"] as const;

export type SettingsTabSlug =
  | (typeof SHARED_SETTINGS_TABS)[number]
  | (typeof TOOL_SETTINGS_TABS)[number];

const KNOWN = new Set<string>([...SHARED_SETTINGS_TABS, ...TOOL_SETTINGS_TABS]);

/** Canonical tab for a `?tab=` value: unknown or missing falls back to Account. */
export function resolveSettingsTab(raw: string | null | undefined): SettingsTabSlug {
  const slug = (raw ?? "").trim().toLowerCase();
  return KNOWN.has(slug) ? (slug as SettingsTabSlug) : "account";
}

export function settingsTabHref(slug: SettingsTabSlug): string {
  return slug === "account" ? "/settings" : `/settings?tab=${slug}`;
}
