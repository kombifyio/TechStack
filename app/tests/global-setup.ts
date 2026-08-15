/// <reference types="node" />

import type { FullConfig } from "@playwright/test";
import { execFileSync } from "child_process";
import { requireTechStackTestUser } from "../src/lib/testing/techstack-test-users";

function shouldSkipDockerSetup() {
  return (
    process.env.PLAYWRIGHT_SKIP_DOCKER_SETUP === "1" ||
    process.env.PLAYWRIGHT_REUSE_SERVER === "1"
  );
}

function run(cmd: string, args: string[]) {
  execFileSync(cmd, args, {
    stdio: "inherit",
    env: process.env,
  });
}

function assertDockerAvailable() {
  try {
    // This fails fast when the configured local or remote engine is unavailable.
    run("docker", ["version"]);
  } catch (err) {
    throw new Error(
      "Docker is required for PocketBase integration tests but isn't available. " +
        "Start the local engine or fix DOCKER_HOST, then rerun `npx playwright test`. " +
        `Original error: ${String(err)}`,
    );
  }
}

async function waitForJson(url: string, timeoutMs: number) {
  const started = Date.now();
  let lastErr: unknown;

  while (Date.now() - started < timeoutMs) {
    try {
      const res = await fetch(url, { cache: "no-store" });
      if (!res.ok) throw new Error(`HTTP ${res.status} for ${url}`);
      return (await res.json()) as any;
    } catch (err) {
      lastErr = err;
      await new Promise((r) => setTimeout(r, 1000));
    }
  }
  throw new Error(
    `Timed out waiting for ${url} after ${timeoutMs}ms. Last error: ${String(
      lastErr,
    )}`,
  );
}

function getContainerId(composeFile: string, service: string): string {
  const id = execFileSync(
    "docker",
    ["compose", "-f", composeFile, "ps", "-q", service],
    {
      encoding: "utf8",
      env: process.env,
    },
  ).trim();

  if (!id) {
    throw new Error(`Could not resolve container id for service '${service}'`);
  }
  return id;
}

function getContainerHealthStatus(containerId: string): string {
  // If the container has no healthcheck, .State.Health will be empty.
  // We treat that as "unknown" and let callers decide.
  return execFileSync(
    "docker",
    [
      "inspect",
      "--format",
      "{{if .State.Health}}{{.State.Health.Status}}{{else}}unknown{{end}}",
      containerId,
    ],
    { encoding: "utf8", env: process.env },
  ).trim();
}

async function waitForHealthy(
  composeFile: string,
  service: string,
  timeoutMs: number,
) {
  const containerId = getContainerId(composeFile, service);
  const started = Date.now();
  let lastStatus = "unknown";

  while (Date.now() - started < timeoutMs) {
    lastStatus = getContainerHealthStatus(containerId);
    if (lastStatus === "healthy") return;
    await new Promise((r) => setTimeout(r, 1000));
  }

  throw new Error(
    `Timed out waiting for '${service}' to become healthy after ${timeoutMs}ms (last status: ${lastStatus}).`,
  );
}

export default async function globalSetup(_: FullConfig) {
  requireTechStackTestUser("admin");

  if (shouldSkipDockerSetup()) {
    return;
  }

  const composeFile =
    process.env.PLAYWRIGHT_COMPOSE_FILE ?? "../docker-compose.yml";

  assertDockerAvailable();

  // Ensure required backend services are running. This makes the PocketBase
  // integration test meaningful and avoids false failures when PB/API aren't up.
  try {
    run("docker", [
      "compose",
      "-f",
      composeFile,
      "up",
      "-d",
      "--build",
      "techstack",
      "app",
    ]);
  } catch (err) {
    throw new Error(
      "Failed to start required services via docker compose. " +
        "Ensure the configured engine and `docker compose` are available. " +
        `Original error: ${String(err)}`,
    );
  }

  // Wait for container health checks (avoids hardcoding any host/port).
  await waitForHealthy(composeFile, "techstack", 180_000);
  await waitForHealthy(composeFile, "app", 180_000);
}
