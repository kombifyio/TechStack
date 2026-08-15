import { spawn, spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";

const appDir = path.dirname(fileURLToPath(import.meta.url));
const rootDir = path.resolve(appDir, "..");
const launcherScript = path.join(
  rootDir,
  "scripts",
  "playwright-no-setup-webserver.mjs",
);
const playwrightBin =
  process.platform === "win32"
    ? path.join(rootDir, "node_modules", ".bin", "playwright.cmd")
    : path.join(rootDir, "node_modules", ".bin", "playwright");

const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:5261";
const env = {
  ...process.env,
  PLAYWRIGHT_BASE_URL: baseURL,
  PLAYWRIGHT_REUSE_SERVER: "1",
};

function spawnLauncher() {
  return spawn(process.execPath, [launcherScript], {
    cwd: rootDir,
    env,
    stdio: "inherit",
  });
}

function spawnPlaywright(extraArgs) {
  if (process.platform === "win32") {
    const command = [
      "npm exec playwright -- test --config=playwright.nosetup.config.ts",
      ...extraArgs,
    ].join(" ");

    return spawn(
      process.env.ComSpec || "cmd.exe",
      ["/d", "/s", "/c", command],
      {
        cwd: rootDir,
        env,
        stdio: "inherit",
      },
    );
  }

  return spawn(
    playwrightBin,
    ["test", "--config=playwright.nosetup.config.ts", ...extraArgs],
    {
      cwd: rootDir,
      env,
      stdio: "inherit",
    },
  );
}

async function waitForReady(url, timeoutMs) {
  const started = Date.now();
  let lastError = "";

  while (Date.now() - started < timeoutMs) {
    try {
      const response = await fetch(`${url}/api/v1/auth/mode`, {
        cache: "no-store",
      });
      if (response.ok) {
        return;
      }
      lastError = `HTTP ${response.status}`;
    } catch (error) {
      lastError = error instanceof Error ? error.message : String(error);
    }
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }

  throw new Error(
    `Timed out waiting for no-setup Playwright server at ${url}. Last error: ${lastError}`,
  );
}

async function waitForStablePage(url, timeoutMs) {
  const started = Date.now();
  let lastError = "";
  let consecutiveReady = 0;

  while (Date.now() - started < timeoutMs) {
    try {
      // The app is a static SPA (ADR-033 OQ2): the server returns the CSR
      // shell without page content, so readiness checks the shell marker
      // instead of server-rendered page text.
      const response = await fetch(`${url}/client/local?client=windows`, {
        cache: "no-store",
      });
      const body = await response.text();
      if (response.ok && body.includes("data-sveltekit-preload-data")) {
        consecutiveReady += 1;
        if (consecutiveReady >= 2) {
          return;
        }
      } else {
        consecutiveReady = 0;
        lastError = `HTTP ${response.status}`;
      }
    } catch (error) {
      consecutiveReady = 0;
      lastError = error instanceof Error ? error.message : String(error);
    }
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }

  throw new Error(
    `Timed out waiting for stable no-setup Playwright page at ${url}. Last error: ${lastError}`,
  );
}

function terminate(child) {
  if (!child || child.killed) {
    return;
  }

  if (process.platform === "win32") {
    spawn("taskkill", ["/pid", String(child.pid), "/t", "/f"], {
      stdio: "ignore",
    });
    cleanupWindowsVitePort();
    return;
  }

  child.kill("SIGTERM");
}

function cleanupWindowsVitePort() {
  if (process.platform !== "win32") return;
  let port = "";
  try {
    port = new URL(baseURL).port;
  } catch {
    return;
  }
  if (!port) return;

  const command = `$port = '${port}'; Get-CimInstance Win32_Process | Where-Object { $_.Name -like 'node*' -and $_.CommandLine -match 'vite' -and $_.CommandLine -match $port } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }`;
  spawnSync(
    "powershell.exe",
    ["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", command],
    { stdio: "ignore" },
  );
}

const launcher = spawnLauncher();

process.on("SIGINT", () => terminate(launcher));
process.on("SIGTERM", () => terminate(launcher));

try {
  await waitForReady(baseURL, 120_000);
  await waitForStablePage(baseURL, 60_000);

  const playwright = spawnPlaywright(process.argv.slice(2));
  const exitCode = await new Promise((resolve, reject) => {
    playwright.on("exit", (code) => resolve(code ?? 0));
    playwright.on("error", reject);
  });

  terminate(launcher);
  process.exit(exitCode);
} catch (error) {
  terminate(launcher);
  throw error;
}
