declare const __APP_VERSION__: string;
declare const __APP_COMMIT__: string;
declare const __APP_BUILD_TIME__: string;

export const appVersion = __APP_VERSION__;

// Commit SHA injected at build time (RENDER_GIT_COMMIT et al via
// vite.config.ts define). Empty in dev; the visible label shortens a verified
// full SHA to seven characters. Matches the contract in
// kombify-AI/apps/portal/src/lib/version.ts and
// kombify-simulate/app/src/lib/config.ts so the deploy identity is rendered
// consistently across the three SvelteKit surfaces (Marcel 2026-05-24).
export const appCommit: string =
  typeof __APP_COMMIT__ !== "undefined" && __APP_COMMIT__ ? __APP_COMMIT__ : "";

export const appBuildTime: string =
  typeof __APP_BUILD_TIME__ !== "undefined" && __APP_BUILD_TIME__
    ? __APP_BUILD_TIME__
    : "";

export function productIdentityLabel(
  version: string | null | undefined,
  revision: string | null | undefined,
): string {
  const normalizedVersion = String(version ?? "").trim();
  const normalizedRevision = String(revision ?? "")
    .trim()
    .toLowerCase();
  if (!normalizedVersion) {
    return "";
  }
  return /^[0-9a-f]{40}$/.test(normalizedRevision)
    ? `${normalizedVersion} · ${normalizedRevision.slice(0, 7)}`
    : normalizedVersion;
}

// Canonical visible deploy label. UI components render it unchanged.
// Format:
//   "0.3.0 · abc1234"  (semver + 7-char commit)
//   "0.3.0"            (dev)
export const appDeployLabel: string = productIdentityLabel(
  appVersion,
  appCommit,
);
