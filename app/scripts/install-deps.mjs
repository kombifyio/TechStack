#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const appRoot = resolve(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
  "app",
);
const pnpmArgs = process.argv
  .slice(2)
  .map((arg) => (arg === "--production" ? "--prod" : arg));

const result = spawnSync("pnpm", ["install", ...pnpmArgs], {
  cwd: appRoot,
  stdio: "inherit",
  shell: process.platform === "win32",
});

if (result.error) {
  console.error(`ERROR: failed to run pnpm install: ${result.error.message}`);
}

process.exit(result.status ?? 1);
