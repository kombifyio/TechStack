#!/usr/bin/env node
// Browser lane of the affected development gate (scripts/affected-dev-gate.mjs).
// Runs the mocked UI specs in tests/ui against the static production build
// served next to the in-process mock backend. The build replaces `vite dev`
// on purpose: on-demand compilation and dependency re-optimization made cold
// runs render blank pages under parallel workers.
//
// Usage: node app/scripts/run-ui-gate.mjs [tests/ui/<file>.spec.ts ...]
// With no spec arguments the whole UI suite runs.
import { spawnSync } from "node:child_process";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const appDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const repoRoot = path.resolve(appDir, "..");
const isWindows = process.platform === "win32";

function run(label, command, args, env = process.env) {
  console.log(`[ui-gate] ${label}`);
  const result = spawnSync(command, args, {
    cwd: appDir,
    env,
    stdio: "inherit",
    // pnpm is a .cmd shim on Windows.
    shell: isWindows && command === "pnpm",
  });
  if (result.status !== 0) {
    process.exit(result.status ?? 1);
  }
}

run("provision chromium", process.execPath, [
  path.join(repoRoot, "scripts", "ensure-playwright-chromium.mjs"),
]);
run("build the static app", "pnpm", ["build"]);
run(
  "run the mocked UI specs",
  process.execPath,
  [
    path.join(appDir, "scripts", "run-playwright-nosetup.mjs"),
    "--project",
    "chromium",
    "--reporter",
    process.env.CI === "true" ? "github" : "line",
    ...process.argv.slice(2),
  ],
  { ...process.env, PLAYWRIGHT_NO_SETUP_SERVER_MODE: "build" },
);
