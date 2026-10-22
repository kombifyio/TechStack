import { sveltekit } from "@sveltejs/kit/vite";
import path from "node:path";
import { readFileSync } from "node:fs";
import { defineConfig } from "vitest/config";

const appVersion = readFileSync(
  new URL("../VERSION", import.meta.url),
  "utf-8",
).trim();

export default defineConfig({
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
});
