import { sveltekit } from "@sveltejs/kit/vite";
import adapterStatic from "@sveltejs/adapter-static";
import { vitePreprocess } from "@sveltejs/vite-plugin-svelte";
import { sentrySvelteKit } from "@sentry/sveltekit";
import tailwindcss from "@tailwindcss/vite";
import { mdsvex } from "mdsvex";
import { readFileSync } from "node:fs";
import path from "node:path";
import { defineConfig } from "vite-plus";

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
  // Go-owned root redirects (web convergence). Exact paths only;
  // /auth/sso and /auth/cloud-link-complete remain frontend routes.
  "/auth/callback": devBackendTarget,
  "/auth/logout": devBackendTarget,
  "/docs": devBackendTarget,
} as const;

// Public settings that differ between Preview and Stable are runtime values:
// Go injects window.__TECHSTACK_PUBLIC_CONFIG__ into index.html from the
// serving process environment (internal/frontend/frontend.go), so one image
// serves every channel. A build maps each import.meta.env read to that object
// and keeps a build-time value only as the fallback for a page Go did not serve.
const publicRuntimeConfigNames = [
  "VITE_GATEWAY_API_BASE",
  "VITE_AUTH0_DOMAIN",
  "VITE_AUTH0_SPA_CLIENT_ID",
  "VITE_AUTH0_AUDIENCE",
] as const;

function publicRuntimeConfigDefine(): Record<string, string> {
  return Object.fromEntries(
    publicRuntimeConfigNames.map((name) => [
      `import.meta.env.${name}`,
      `(globalThis.__TECHSTACK_PUBLIC_CONFIG__?.${name} || ${JSON.stringify(process.env[name] || "")})`,
    ]),
  );
}

// Vite+ (D-2026-10-30-06): `vp check` reads the `lint` and `fmt` blocks, `vp test` the test
// branch at the end (Vitest runs in mode "test"), `vp dev` and `vp build` the app branch. Only
// .ts/.js sources are in scope; .svelte files stay on the ESLint and Prettier bridge and
// svelte-check until Oxc reaches Svelte parity, and Prettier keeps owning formatting.
const checkIgnorePatterns = [
  "build/**",
  "build-static/**",
  ".e2e/**",
  ".svelte-kit/**",
  "dist/**",
  "node_modules/**",
  "test-results*/**",
  "playwright-report*/**",
  // Outside the TypeScript project (tsconfig.json), so the type-aware lint leaves them alone.
  "playwright*.config.ts",
  "**/*.{md,json,jsonc,yml,yaml,toml,css,html,svelte}",
];

const toolchain = {
  fmt: { ignorePatterns: checkIgnorePatterns },
  lint: {
    ignorePatterns: checkIgnorePatterns,
    options: { typeAware: true, typeCheck: true },
  },
};

const appConfig = async ({ command }: { command: string }) =>
  defineConfig({
    ...toolchain,
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
      // SvelteKit 3 folds the former svelte.config.js into these plugin
      // options (the file is no longer supported).
      sveltekit({
        extensions: [".svelte", ".md"],

        preprocess: [
          vitePreprocess(),
          mdsvex({
            extensions: [".md"],
          }),
        ],

        // Single build for every deployment (ADR-033 OQ2 web convergence): a
        // static SPA in build-static, embedded into the Go binary via
        // internal/frontend/dist (-tags techstack_static_ui) and served by Go.
        // The legacy TECHSTACK_DESKTOP_STATIC toggle is accepted but ignored —
        // the static build is no longer desktop-only.
        adapter: adapterStatic({
          pages: "build-static",
          assets: "build-static",
          fallback: "index.html",
          strict: false,
        }),
      }),
    ],
    define: {
      __APP_VERSION__: JSON.stringify(appVersion),
      __APP_COMMIT__: JSON.stringify(buildRevision),
      __APP_BUILD_TIME__: JSON.stringify(new Date().toISOString()),
      __SENTRY_RELEASE__: JSON.stringify(sentryRelease),
      ...(command === "build" ? publicRuntimeConfigDefine() : {}),
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

// Unit tests (formerly vitest.config.ts): the plain SvelteKit plugin and the mocks for the
// two SvelteKit 3 modules the units import.
const testConfig = {
  ...toolchain,
  plugins: [sveltekit()],
  test: {
    environment: "node",
    include: ["src/**/*.test.ts", "src/**/*.spec.ts"],
    exclude: ["tests/**", "node_modules/**", "dist/**", "build/**"],
    globals: true,
    maxWorkers: 1,
  },
  define: {
    __APP_VERSION__: JSON.stringify(appVersion),
    __APP_COMMIT__: JSON.stringify(""),
    __APP_BUILD_TIME__: JSON.stringify(""),
  },
  resolve: {
    alias: {
      // SvelteKit 3: `$lib` is gone (package.json "imports" owns `#lib`) and
      // `$app/environment` was renamed to `$app/env`.
      "$app/env": path.resolve(
        import.meta.dirname,
        "test/mocks/app-environment.ts",
      ),
      "$app/navigation": path.resolve(
        import.meta.dirname,
        "test/mocks/app-navigation.ts",
      ),
    },
  },
};

export default defineConfig(({ command, mode }) =>
  mode === "test" ? testConfig : appConfig({ command }),
);
