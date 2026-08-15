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
    coverage: {
      provider: "v8",
      reporter: ["text", "json", "html", "lcov"],
      all: true,
      exclude: [
        "node_modules/",
        ".svelte-kit/",
        "dist/",
        "build/",
        "coverage/",
        "tests/",
        "**/*.config.{js,ts}",
        "**/*.d.ts",
        "**/*.{test,spec}.{js,ts}",
        "**/mocks/**",
      ],
      thresholds: { lines: 50, functions: 50, branches: 50, statements: 50 },
    },
  },
  define: {
    __APP_VERSION__: JSON.stringify(appVersion),
    __APP_COMMIT__: JSON.stringify(""),
    __APP_BUILD_TIME__: JSON.stringify(""),
  },
  resolve: {
    alias: {
      $lib: path.resolve(__dirname, "src/lib"),
      "$app/environment": path.resolve(
        __dirname,
        "test/mocks/app-environment.ts",
      ),
      "$app/navigation": path.resolve(
        __dirname,
        "test/mocks/app-navigation.ts",
      ),
    },
  },
});
