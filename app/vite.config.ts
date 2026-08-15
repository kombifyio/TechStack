import { sveltekit } from "@sveltejs/kit/vite";
import { sentrySvelteKit } from "@sentry/sveltekit";
import tailwindcss from "@tailwindcss/vite";
import { readFileSync } from "node:fs";
import { defineConfig } from "vite";

const appVersion = readFileSync(
  new URL("../VERSION", import.meta.url),
  "utf-8",
).trim();
if (
  !/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/.test(
    appVersion,
  )
) {
  throw new Error(
    `Committed VERSION must be SemVer; got ${JSON.stringify(appVersion)}`,
  );
}

function resolveBuildRevision(): string {
  const candidates = [
    process.env.RENDER_GIT_COMMIT,
    process.env.GIT_COMMIT,
    process.env.GITHUB_SHA,
  ]
    .map((value) => value?.trim().toLowerCase() || "")
    .filter(Boolean);
  const revisions = [...new Set(candidates)];
  if (revisions.length > 1) {
    throw new Error("Build revision inputs identify different Git commits");
  }
  const revision = revisions[0] || "";
  if (revision && !/^[0-9a-f]{40}$/.test(revision)) {
    throw new Error("Build revision must be a full 40-character Git SHA");
  }
  return revision;
}

const buildRevision = resolveBuildRevision();
const sentryRelease =
  process.env.SENTRY_RELEASE ||
  process.env.PUBLIC_SENTRY_RELEASE ||
  buildRevision ||
  `kombify-techstack@${appVersion}`;

process.env.SENTRY_RELEASE ||= sentryRelease;
process.env.PUBLIC_SENTRY_RELEASE ||= sentryRelease;

const shouldUploadSourceMaps = Boolean(
  process.env.SENTRY_AUTH_TOKEN && process.env.SENTRY_ORG,
);

// Backend origin for the dev/preview server proxy (see overrides below).
const devBackendTarget =
  process.env.TECHSTACK_DEV_API_PROXY ||
  process.env.TECHSTACK_API_URL ||
  "http://127.0.0.1:5260";
const devBackendProxy = {
  "/api": devBackendTarget,
  "/install.sh": devBackendTarget,
  "/install.ps1": devBackendTarget,
  // Go-owned root redirects (web convergence). Exact paths only — other
  // /auth/* routes (oidc-complete, sso, cloud-link-complete) are SPA pages.
  "/auth/callback": devBackendTarget,
  "/auth/logout": devBackendTarget,
  "/docs": devBackendTarget,
} as const;

export default async () =>
  defineConfig({
    plugins: [
      tailwindcss(),
      ...(await sentrySvelteKit({
        // adapter-static build (ADR-033 OQ2): no Node SSR runtime to instrument.
        adapter: "other",
        autoUploadSourceMaps: shouldUploadSourceMaps,
        org: process.env.SENTRY_ORG,
        project: process.env.SENTRY_PROJECT || "kombify-techstack",
        sentryUrl: process.env.SENTRY_URL,
        authToken: process.env.SENTRY_AUTH_TOKEN,
        release: {
          name: sentryRelease,
        },
        telemetry: false,
        silent: process.env.SENTRY_DEBUG !== "1",
        errorHandler(error) {
          if (process.env.SENTRY_SOURCE_MAPS_REQUIRED === "1") {
            throw error;
          }
          console.warn(`[Sentry] Source map upload skipped: ${error.message}`);
        },
      })),
      sveltekit(),
    ],
    define: {
      __APP_VERSION__: JSON.stringify(appVersion),
      __APP_COMMIT__: JSON.stringify(buildRevision),
      __APP_BUILD_TIME__: JSON.stringify(new Date().toISOString()),
      __SENTRY_RELEASE__: JSON.stringify(sentryRelease),
    },
    server: {
      fs: {
        allow: [".."],
      },
      // Dev-only backend proxy. The retired SvelteKit api/[...path] SSR
      // route used to forward same-origin /api calls to the Go backend; in
      // the static-SPA world (ADR-033 OQ2) vite dev owns that concern so
      // `pnpm dev` / `mise run dev:app` keep working against a local
      // backend. Production serves the SPA from the Go binary directly.
      proxy: devBackendProxy,
    },
    preview: {
      proxy: devBackendProxy,
    },
  });
