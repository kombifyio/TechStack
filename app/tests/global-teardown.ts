/// <reference types="node" />

import type { FullConfig } from "@playwright/test";
import { execFileSync } from "child_process";

function run(cmd: string, args: string[]) {
  execFileSync(cmd, args, {
    stdio: "inherit",
    env: process.env,
  });
}

export default async function globalTeardown(_: FullConfig) {
  // Keep services running locally when debugging.
  if (
    process.env.PLAYWRIGHT_KEEP_DOCKER === "1" ||
    process.env.PLAYWRIGHT_SKIP_DOCKER_SETUP === "1" ||
    process.env.PLAYWRIGHT_REUSE_SERVER === "1"
  ) {
    return;
  }

  const composeFile =
    process.env.PLAYWRIGHT_COMPOSE_FILE ?? "../docker-compose.yml";
  try {
    run("docker", ["compose", "-f", composeFile, "down", "-v", "-t", "10"]);
  } catch {
    // Ignore teardown failures (e.g., Docker not running). Setup already emitted
    // a clear error in those cases.
  }
}
