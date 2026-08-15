import { test, expect, type Page, type BrowserContext } from "@playwright/test";
import { execSync } from "child_process";
import { mkdirSync, writeFileSync } from "fs";
import path from "path";
import {
  TEST_CREDENTIALS,
  requireApiBase,
  authenticateViaApi,
  createPairingTokenViaApi,
  registerWorkerViaApi,
  approveWorkerViaApi,
  login,
  waitForPageLoad,
} from "../helpers/test-utils";

const API_BASE = requireApiBase();
const STACKKITS_API_URL =
  process.env.STACKKITS_API_URL ?? "http://localhost:8082";
const STRICT_SCREENSHOTS = process.env.ORCHESTRATION_STRICT_SCREENSHOTS === "1";
const REQUIRE_DEFAULT_ROUTES =
  process.env.OSS_BYOS_REQUIRE_DEFAULT_ROUTES === "1";
const DEFAULT_BASE_KIT_ROUTES = [
  { name: "Node Hub", host: "base.home.localhost", path: "/" },
  { name: "Pocket ID", host: "id.home.localhost", path: "/" },
  { name: "TinyAuth", host: "auth.home.localhost", path: "/" },
  { name: "Uptime Kuma", host: "kuma.home.localhost", path: "/" },
  { name: "Whoami", host: "whoami.home.localhost", path: "/" },
  { name: "Vaultwarden", host: "vault.home.localhost", path: "/" },
  { name: "Immich", host: "photos.home.localhost", path: "/" },
] as const;
const ROUTE_REACHABLE_STATUS = new Set([
  200, 204, 301, 302, 303, 307, 308, 401, 403,
]);

type ScreenshotOptions = {
  fullPage?: boolean;
  maxDiffPixelRatio?: number;
};

type RouteProbeResult = {
  name: string;
  host: string;
  path: string;
  status: number;
  contentType: string;
  timeSeconds: number | null;
  output: string;
};

type RouteProbeEvidence = {
  required: boolean;
  acceptedStatuses: number[];
  routes: RouteProbeResult[];
  vm: {
    containers: string;
    listeners: string;
  };
};

/**
 * Execute a command inside the StackKits VM container.
 */
function execInVM(command: string, timeout = 30_000): string {
  return execSync(
    `docker exec stackkits-vm sh -c "${command.replace(/"/g, '\\"')}"`,
    {
      encoding: "utf-8",
      timeout,
    },
  );
}

function parseRouteProbeOutput(
  route: (typeof DEFAULT_BASE_KIT_ROUTES)[number],
  output: string,
): RouteProbeResult {
  const status = Number.parseInt(output.match(/STATUS:(\d{3})/)?.[1] ?? "0");
  const timeRaw = output.match(/TIME:([0-9.]+)/)?.[1];
  return {
    name: route.name,
    host: route.host,
    path: route.path,
    status,
    contentType: output.match(/TYPE:([^\n]*)/)?.[1]?.trim() ?? "",
    timeSeconds: timeRaw ? Number.parseFloat(timeRaw) : null,
    output: output.trim(),
  };
}

function probeDefaultRoute(
  route: (typeof DEFAULT_BASE_KIT_ROUTES)[number],
): RouteProbeResult {
  const bodyPath = `/tmp/techstack-route-probe-${route.host.replace(/[^a-z0-9]/gi, "-")}.body`;
  const output = execInVM(
    `curl -sS -o ${bodyPath} -w "STATUS:%{http_code}\\nTYPE:%{content_type}\\nTIME:%{time_total}\\n" -H "Host: ${route.host}" --max-time 15 "http://127.0.0.1${route.path}" 2>&1 || true`,
    20_000,
  );
  return parseRouteProbeOutput(route, output);
}

function collectRouteProbeContext(): RouteProbeEvidence["vm"] {
  return {
    containers: execInVM(
      `docker ps --format "table {{.Names}}\\t{{.Image}}\\t{{.Status}}\\t{{.Ports}}" || true`,
    ).trim(),
    listeners: execInVM(
      "ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null || true",
    ).trim(),
  };
}

async function attachEvidenceJson(name: string, data: unknown) {
  const body = Buffer.from(`${JSON.stringify(data, null, 2)}\n`, "utf8");
  await test.info().attach(name, {
    body,
    contentType: "application/json",
  });

  if (process.env.OSS_BYOS_EVIDENCE_DIR) {
    mkdirSync(process.env.OSS_BYOS_EVIDENCE_DIR, { recursive: true });
    writeFileSync(path.join(process.env.OSS_BYOS_EVIDENCE_DIR, name), body);
  }
}

/**
 * Escape HTML entities for safe embedding in pages.
 */
function escapeHtml(str: string): string {
  return str
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

async function captureEvidenceScreenshot(
  page: Page,
  screenshotName: string,
  options: ScreenshotOptions = {},
) {
  if (STRICT_SCREENSHOTS) {
    await expect(page).toHaveScreenshot(screenshotName, {
      fullPage: options.fullPage ?? true,
      maxDiffPixelRatio: options.maxDiffPixelRatio,
    });
    return;
  }

  const screenshot = await page.screenshot({
    fullPage: options.fullPage ?? true,
  });
  await test.info().attach(screenshotName, {
    body: screenshot,
    contentType: "image/png",
  });

  if (process.env.OSS_BYOS_EVIDENCE_DIR) {
    mkdirSync(process.env.OSS_BYOS_EVIDENCE_DIR, { recursive: true });
    writeFileSync(
      path.join(process.env.OSS_BYOS_EVIDENCE_DIR, screenshotName),
      screenshot,
    );
  }
}

/**
 * Render terminal output as a styled HTML page and capture a screenshot.
 */
async function screenshotTerminalOutput(
  ctx: BrowserContext,
  title: string,
  sections: { heading: string; content: string }[],
  screenshotName: string,
) {
  const page = await ctx.newPage();
  const sectionsHtml = sections
    .map(
      (s) => `
      <h3 style="color:#8b949e;margin-top:16px;margin-bottom:8px">${escapeHtml(s.heading)}</h3>
      <pre style="background:#161b22;padding:15px;border-radius:8px;font-size:12px;line-height:1.4;overflow:auto;white-space:pre-wrap">${escapeHtml(s.content)}</pre>
    `,
    )
    .join("");

  await page.setContent(`
    <html><body style="background:#0d1117;color:#c9d1d9;font-family:'Cascadia Code','Fira Code',monospace;padding:24px;min-width:1200px">
      <h2 style="color:#58a6ff;margin-bottom:12px">${escapeHtml(title)}</h2>
      ${sectionsHtml}
    </body></html>
  `);

  await captureEvidenceScreenshot(page, screenshotName, { fullPage: true });
  await page.close();
}

test.describe.serial("Orchestration Flow (Stack + StackKits VM)", () => {
  let sharedPage: Page;
  let ctx: BrowserContext;
  let authToken: string;

  test.beforeAll(async ({ browser }) => {
    ctx = await browser.newContext();
    sharedPage = await ctx.newPage();
  });

  test.afterAll(async () => {
    if (ctx) await ctx.close();
  });

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // Phase 1: Health & Connectivity
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  test("01 - Stack API health", async ({ request }) => {
    const res = await request.get(`${API_BASE}/api/v1/health`);
    expect(res.ok()).toBeTruthy();
    const json = await res.json();
    expect(json.data?.status ?? json.status).toBe("healthy");
  });

  test("02 - StackKits API health", async ({ request }) => {
    const res = await request.get(`${STACKKITS_API_URL}/health`);
    expect(res.ok()).toBeTruthy();
  });

  test("03 - StackKits API capabilities", async ({ request }) => {
    const res = await request.get(`${STACKKITS_API_URL}/api/v1/capabilities`);
    expect(res.ok()).toBeTruthy();
    const json = await res.json();
    expect(json).toBeTruthy();
  });

  test("04 - StackKits catalog listing", async ({ request }) => {
    const res = await request.get(`${STACKKITS_API_URL}/api/v1/stackkits`);
    expect(res.ok()).toBeTruthy();
    const json = await res.json();
    const items = json.data?.items ?? json.data ?? json;
    expect(Array.isArray(items) ? items.length : 0).toBeGreaterThan(0);
  });

  test("05 - VM Docker daemon is running", async () => {
    const info = execInVM("docker info --format '{{.ServerVersion}}'");
    expect(info.trim()).toBeTruthy();
  });

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // Phase 2: VM Baseline (pre-deployment)
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  test("06 - VM system info baseline screenshot", async () => {
    const cpus = execInVM("nproc").trim();
    const uname = execInVM("uname -a").trim();
    const memInfo = execInVM("free -h");
    const diskInfo = execInVM("df -h /");
    const dockerVersion = execInVM(
      "docker version --format '{{.Server.Version}}'",
    ).trim();
    const dockerContainers = execInVM("docker ps -a -q | wc -l").trim();

    await screenshotTerminalOutput(
      ctx,
      "StackKits VM - System Baseline (Pre-Deployment)",
      [
        {
          heading: `CPU: ${cpus} cores | Docker: v${dockerVersion} | Containers: ${dockerContainers}`,
          content: uname,
        },
        { heading: "Memory", content: memInfo },
        { heading: "Disk", content: diskInfo },
      ],
      "orchestration-01-vm-baseline.png",
    );
  });

  test("07 - VM Docker images baseline", async () => {
    const images = execInVM(
      "docker images --format 'table {{.Repository}}\\t{{.Tag}}\\t{{.Size}}'",
    );

    await screenshotTerminalOutput(
      ctx,
      "StackKits VM - Docker Images (Pre-Deployment)",
      [{ heading: "Images", content: images || "(no images)" }],
      "orchestration-02-vm-images-before.png",
    );
  });

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // Phase 3: Login & Dashboard (pre-deployment)
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  test("08 - Login to Stack UI", async () => {
    await login(sharedPage);
    await expect(sharedPage.locator("aside").first()).toBeVisible();
  });

  test("09 - Acquire API auth token", async () => {
    const result = await authenticateViaApi(API_BASE);
    authToken = result.token;
    expect(authToken).toBeTruthy();
  });

  test("10 - Dashboard before deployment screenshot", async () => {
    await sharedPage.goto("/stacks");
    await waitForPageLoad(sharedPage);

    await expect(
      sharedPage.getByRole("heading", {
        name: "kombify-TechStack",
        exact: true,
      }),
    ).toBeVisible();
    const sidebar = sharedPage.locator("aside").first();
    await expect(sidebar).toBeVisible();

    await captureEvidenceScreenshot(
      sharedPage,
      "orchestration-03-dashboard-before.png",
      { fullPage: true, maxDiffPixelRatio: 0.1 },
    );
  });

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // Phase 4: Homelab Creation via Wizard
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  test("11 - Wizard start screenshot", async () => {
    await sharedPage.goto("/stacks/new");
    await sharedPage.waitForLoadState("domcontentloaded");
    await sharedPage.waitForTimeout(2000);

    if (!sharedPage.url().includes("/stacks/new")) {
      test.skip(true, "Redirected — stack may already exist");
      return;
    }

    await captureEvidenceScreenshot(
      sharedPage,
      "orchestration-04-wizard-start.png",
      { fullPage: true, maxDiffPixelRatio: 0.1 },
    );
  });

  test("12 - Wizard step: Features", async () => {
    if (!sharedPage.url().includes("/stacks/new")) {
      test.skip(true, "Not on wizard page");
      return;
    }

    const hydrated = sharedPage.getByTestId("hydrated");
    await hydrated
      .waitFor({ state: "attached", timeout: 10_000 })
      .catch(() => null);

    const feature = sharedPage.locator('[data-testid^="easy-feature-"]');
    if ((await feature.count()) > 0) {
      await feature.first().click();
    }

    await captureEvidenceScreenshot(
      sharedPage,
      "orchestration-05-wizard-features.png",
      { fullPage: true, maxDiffPixelRatio: 0.1 },
    );

    const nextBtn = sharedPage.getByTestId("wizard-next");
    if (await nextBtn.isVisible({ timeout: 3_000 }).catch(() => false)) {
      await nextBtn.click();
    }
  });

  test("13 - Wizard step: Access", async () => {
    if (!sharedPage.url().includes("/stacks/new")) {
      test.skip(true, "Not on wizard page");
      return;
    }

    const accessHome = sharedPage.getByTestId("easy-access-home");
    if (await accessHome.isVisible({ timeout: 5_000 }).catch(() => false)) {
      await accessHome.click();

      await captureEvidenceScreenshot(
        sharedPage,
        "orchestration-06-wizard-access.png",
        { fullPage: true, maxDiffPixelRatio: 0.1 },
      );

      await sharedPage.getByTestId("wizard-next").click();
    }
  });

  test("14 - Wizard step: Users", async () => {
    if (!sharedPage.url().includes("/stacks/new")) {
      test.skip(true, "Not on wizard page");
      return;
    }

    const usersMe = sharedPage.getByTestId("easy-users-me");
    if (await usersMe.isVisible({ timeout: 5_000 }).catch(() => false)) {
      await usersMe.click();

      await captureEvidenceScreenshot(
        sharedPage,
        "orchestration-07-wizard-users.png",
        { fullPage: true, maxDiffPixelRatio: 0.1 },
      );

      await sharedPage.getByTestId("wizard-next").click();
    }
  });

  test("15 - Wizard step: Create", async () => {
    if (!sharedPage.url().includes("/stacks/new")) {
      test.skip(true, "Not on wizard page");
      return;
    }

    const createBtn = sharedPage.getByTestId("wizard-create");
    if (await createBtn.isVisible({ timeout: 5_000 }).catch(() => false)) {
      await captureEvidenceScreenshot(
        sharedPage,
        "orchestration-08-wizard-create.png",
        { fullPage: true, maxDiffPixelRatio: 0.1 },
      );

      await createBtn.click();
    }

    await sharedPage
      .waitForURL(/\/stacks\/creating/, { timeout: 15_000 })
      .catch(() => null);
  });

  test("16 - Stack creation in progress screenshot", async () => {
    if (!sharedPage.url().includes("/stacks/creating")) {
      test.skip(true, "Not on creating page");
      return;
    }

    await captureEvidenceScreenshot(
      sharedPage,
      "orchestration-09-creating-progress.png",
      { fullPage: true, maxDiffPixelRatio: 0.2 },
    );

    // Wait for completion (up to 3 minutes)
    const completed = await sharedPage
      .getByText("created")
      .first()
      .waitFor({ state: "visible", timeout: 180_000 })
      .then(() => true)
      .catch(() => false);

    if (completed) {
      await captureEvidenceScreenshot(
        sharedPage,
        "orchestration-10-creation-complete.png",
        { fullPage: true, maxDiffPixelRatio: 0.1 },
      );
    }
  });

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // Phase 5: VM Post-Deployment Inspection
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  test("17 - VM running containers screenshot", async () => {
    const dockerPs = execInVM(
      "docker ps --format 'table {{.Names}}\\t{{.Status}}\\t{{.Ports}}'",
    );
    expect(dockerPs).toBeTruthy();

    const containerCount = execInVM("docker ps -q | wc -l").trim();

    await screenshotTerminalOutput(
      ctx,
      `StackKits VM - Running Containers (${containerCount} total)`,
      [{ heading: "docker ps", content: dockerPs }],
      "orchestration-11-vm-containers.png",
    );
  });

  test("18 - VM container health status screenshot", async () => {
    const healthOutput = execInVM(
      "docker ps --format '{{.Names}}\\t{{.Status}}'",
    );

    // Check for healthy/unhealthy markers
    const inspectOutput = execInVM(
      "docker inspect --format '{{.Name}} → {{if .State.Health}}{{.State.Health.Status}}{{else}}no-healthcheck{{end}}' $(docker ps -q) 2>/dev/null || echo 'no containers'",
    );

    await screenshotTerminalOutput(
      ctx,
      "StackKits VM - Container Health Status",
      [
        { heading: "Container Status", content: healthOutput },
        { heading: "Health Checks", content: inspectOutput },
      ],
      "orchestration-12-vm-health.png",
    );
  });

  test("19 - VM Docker stats screenshot", async () => {
    const statsOutput = execInVM(
      "docker stats --no-stream --format 'table {{.Name}}\\t{{.CPUPerc}}\\t{{.MemUsage}}\\t{{.MemPerc}}\\t{{.NetIO}}\\t{{.BlockIO}}'",
    );

    await screenshotTerminalOutput(
      ctx,
      "StackKits VM - Docker Stats (CPU, Memory, Network, I/O)",
      [{ heading: "docker stats", content: statsOutput }],
      "orchestration-13-vm-docker-stats.png",
    );
  });

  test("20 - VM Docker networks screenshot", async () => {
    const networks = execInVM(
      "docker network ls --format 'table {{.Name}}\\t{{.Driver}}\\t{{.Scope}}'",
    );

    // Inspect non-default networks for connected containers
    const customNets = execInVM(
      "docker network ls --format '{{.Name}}' | grep -v -E '^(bridge|host|none)$' || true",
    ).trim();

    let networkDetails = "";
    if (customNets) {
      for (const net of customNets.split("\n").filter(Boolean)) {
        const detail = execInVM(
          `docker network inspect ${net} --format '{{.Name}}: {{range .Containers}}{{.Name}} {{end}}' 2>/dev/null || true`,
        ).trim();
        if (detail) networkDetails += detail + "\n";
      }
    }

    await screenshotTerminalOutput(
      ctx,
      "StackKits VM - Docker Networks",
      [
        { heading: "Networks", content: networks },
        {
          heading: "Container Connections",
          content: networkDetails || "(no custom networks)",
        },
      ],
      "orchestration-14-vm-networks.png",
    );
  });

  test("21 - VM Docker volumes screenshot", async () => {
    const volumes = execInVM(
      "docker volume ls --format 'table {{.Name}}\\t{{.Driver}}'",
    );

    // Volume sizes
    const volumeSizes = execInVM(
      "docker system df -v 2>/dev/null | grep -A 100 'VOLUME NAME' | head -30 || echo 'no volume info'",
    );

    await screenshotTerminalOutput(
      ctx,
      "StackKits VM - Docker Volumes (Persistent Storage)",
      [
        { heading: "Volumes", content: volumes },
        { heading: "Volume Sizes", content: volumeSizes },
      ],
      "orchestration-15-vm-volumes.png",
    );
  });

  test("22 - VM Docker images screenshot", async () => {
    const images = execInVM(
      "docker images --format 'table {{.Repository}}\\t{{.Tag}}\\t{{.Size}}\\t{{.CreatedSince}}'",
    );

    const totalSize = execInVM(
      "docker system df --format 'Images: {{.Size}} ({{.Reclaimable}} reclaimable)' 2>/dev/null | head -1 || echo ''",
    ).trim();

    await screenshotTerminalOutput(
      ctx,
      `StackKits VM - Docker Images${totalSize ? ` (${totalSize})` : ""}`,
      [{ heading: "docker images", content: images }],
      "orchestration-16-vm-images-after.png",
    );
  });

  test("23 - VM container logs (key services) screenshot", async () => {
    const services = ["traefik", "caprover"];
    const sections: { heading: string; content: string }[] = [];

    for (const svc of services) {
      const hasContainer = execInVM(
        `docker ps --format '{{.Names}}' | grep -i ${svc} || true`,
      ).trim();

      if (hasContainer) {
        const logs = execInVM(
          `docker logs ${hasContainer} --tail 30 2>&1 || true`,
          60_000,
        );
        sections.push({
          heading: `${hasContainer} (last 30 lines)`,
          content: logs,
        });
      }
    }

    if (sections.length === 0) {
      sections.push({
        heading: "No key service containers found",
        content: "Traefik and CapRover containers not detected.",
      });
    }

    await screenshotTerminalOutput(
      ctx,
      "StackKits VM - Service Logs (Key Services)",
      sections,
      "orchestration-17-vm-logs.png",
    );
  });

  test("24 - VM listening ports screenshot", async () => {
    const ports = execInVM(
      "ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null || echo 'port info unavailable'",
    );

    await screenshotTerminalOutput(
      ctx,
      "StackKits VM - Listening Ports",
      [{ heading: "TCP Listeners", content: ports }],
      "orchestration-18-vm-ports.png",
    );
  });

  test("24a - Basement Kit default routes evidence from VM", async () => {
    const results = DEFAULT_BASE_KIT_ROUTES.map((route) =>
      probeDefaultRoute(route),
    );
    const evidence: RouteProbeEvidence = {
      required: REQUIRE_DEFAULT_ROUTES,
      acceptedStatuses: Array.from(ROUTE_REACHABLE_STATUS).sort(),
      routes: results,
      vm: collectRouteProbeContext(),
    };
    await attachEvidenceJson("orchestration-24-default-routes.json", evidence);

    const failed = results.filter(
      (result) => !ROUTE_REACHABLE_STATUS.has(result.status),
    );
    if (failed.length > 0 && !REQUIRE_DEFAULT_ROUTES) {
      test.info().annotations.push({
        type: "route-proof",
        description: `Basement Kit routes are not required in this gate; ${failed.length}/${results.length} route probes failed. Set OSS_BYOS_REQUIRE_DEFAULT_ROUTES=1 to enforce.`,
      });
      return;
    }

    expect(failed, JSON.stringify(evidence, null, 2)).toEqual([]);
  });

  test("25 - VM htop / process tree screenshot", async () => {
    const htopOutput = execInVM(
      "bash -c 'TERM=xterm htop --batch-mode --delay=10 --tree 2>/dev/null | head -50 || top -b -n1 | head -30'",
    );

    const loadAvg = execInVM("cat /proc/loadavg").trim();

    await screenshotTerminalOutput(
      ctx,
      `StackKits VM - Process Monitor (Load: ${loadAvg})`,
      [{ heading: "htop / top", content: htopOutput }],
      "orchestration-19-vm-htop.png",
    );
  });

  test("26 - VM system resources screenshot", async () => {
    const cpus = execInVM("nproc").trim();
    const memInfo = execInVM("free -h");
    const diskInfo = execInVM("df -h /");
    const dockerVersion = execInVM(
      "docker version --format '{{.Server.Version}}'",
    ).trim();
    const uptime = execInVM("uptime").trim();
    const dockerDf = execInVM("docker system df 2>/dev/null || echo ''");

    const page = await ctx.newPage();
    await page.setContent(`
        <html><body style="background:#0d1117;color:#c9d1d9;font-family:monospace;padding:24px;min-width:1200px">
          <h2 style="color:#58a6ff;margin-bottom:16px">StackKits VM - System Resources (Post-Deployment)</h2>
          <div style="display:grid;grid-template-columns:repeat(4,1fr);gap:12px;margin-bottom:16px">
            <div style="background:#161b22;padding:15px;border-radius:8px;text-align:center">
              <div style="color:#8b949e;font-size:11px;margin-bottom:4px">CPU</div>
              <div style="font-size:28px;color:#58a6ff;font-weight:bold">${escapeHtml(cpus)}</div>
              <div style="color:#8b949e;font-size:11px">cores</div>
            </div>
            <div style="background:#161b22;padding:15px;border-radius:8px;text-align:center">
              <div style="color:#8b949e;font-size:11px;margin-bottom:4px">Docker</div>
              <div style="font-size:28px;color:#58a6ff;font-weight:bold">v${escapeHtml(dockerVersion)}</div>
            </div>
            <div style="background:#161b22;padding:15px;border-radius:8px;text-align:center">
              <div style="color:#8b949e;font-size:11px;margin-bottom:4px">Containers</div>
              <div style="font-size:28px;color:#58a6ff;font-weight:bold">${escapeHtml(execInVM("docker ps -q | wc -l").trim())}</div>
              <div style="color:#8b949e;font-size:11px">running</div>
            </div>
            <div style="background:#161b22;padding:15px;border-radius:8px;text-align:center">
              <div style="color:#8b949e;font-size:11px;margin-bottom:4px">Uptime</div>
              <div style="font-size:14px;color:#58a6ff;margin-top:8px">${escapeHtml(uptime)}</div>
            </div>
          </div>
          <h3 style="color:#8b949e;margin-bottom:8px">Memory</h3>
          <pre style="background:#161b22;padding:12px;border-radius:8px;font-size:12px">${escapeHtml(memInfo)}</pre>
          <h3 style="color:#8b949e;margin-top:12px;margin-bottom:8px">Disk</h3>
          <pre style="background:#161b22;padding:12px;border-radius:8px;font-size:12px">${escapeHtml(diskInfo)}</pre>
          <h3 style="color:#8b949e;margin-top:12px;margin-bottom:8px">Docker Disk Usage</h3>
          <pre style="background:#161b22;padding:12px;border-radius:8px;font-size:12px">${escapeHtml(dockerDf)}</pre>
        </body></html>
      `);

    await captureEvidenceScreenshot(page, "orchestration-20-vm-sysinfo.png", {
      fullPage: true,
    });
    await page.close();
  });

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // Phase 6: Stack API Verification
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  test("27 - Stack list via API", async ({ request }) => {
    test.skip(!authToken, "No auth token");

    const res = await request.get(`${API_BASE}/api/v1/stacks`, {
      headers: { Authorization: `Bearer ${authToken}` },
    });
    expect(res.ok()).toBeTruthy();
    const json = await res.json();
    const stacks = json.data ?? json;
    expect(Array.isArray(stacks) ? stacks.length : 0).toBeGreaterThan(0);
  });

  test("28 - Stack health via API", async ({ request }) => {
    const liveRes = await request.get(`${API_BASE}/api/v1/health/live`);
    expect(liveRes.ok()).toBeTruthy();

    const readyRes = await request.get(`${API_BASE}/api/v1/health/ready`);
    expect(readyRes.ok()).toBeTruthy();
  });

  test("29 - Worker registration flow via API", async ({ request }) => {
    test.skip(!authToken, "No auth token");

    // Create pairing token
    const pairingResult = await createPairingTokenViaApi(
      authToken,
      API_BASE,
      "orchestration-test-worker",
    );
    expect(pairingResult.token).toBeTruthy();

    // Register worker
    const registerResult = await registerWorkerViaApi(
      pairingResult.token,
      "orchestration-vm-worker",
      API_BASE,
    );
    expect(registerResult.worker_id).toBeTruthy();

    // Approve worker
    await approveWorkerViaApi(authToken, registerResult.worker_id, API_BASE);

    // Verify worker is approved
    const workerRes = await request.get(`${API_BASE}/api/v1/workers`, {
      headers: { Authorization: `Bearer ${authToken}` },
    });
    expect(workerRes.ok()).toBeTruthy();
    const json = await workerRes.json();
    const workers = json.data ?? json;
    const list = Array.isArray(workers) ? workers : [];
    const found = list.find((w: any) => w.id === registerResult.worker_id);
    expect(found).toBeTruthy();
    expect(found.status).toBe("approved");
  });

  test("30 - StackKits catalog detail via API", async ({ request }) => {
    // Get first stackkit from catalog
    const listRes = await request.get(`${STACKKITS_API_URL}/api/v1/stackkits`);
    expect(listRes.ok()).toBeTruthy();
    const listJson = await listRes.json();
    const items = listJson.data?.items ?? listJson.data ?? listJson;
    const kitList = Array.isArray(items) ? items : [];

    if (kitList.length === 0) {
      test.skip(true, "No stackkits in catalog");
      return;
    }

    const firstName = kitList[0].name ?? kitList[0];

    // Get detail
    const detailRes = await request.get(
      `${STACKKITS_API_URL}/api/v1/stackkits/${firstName}`,
    );
    expect(detailRes.ok()).toBeTruthy();

    const schemaRes = await request.get(
      `${STACKKITS_API_URL}/api/v1/stackkits/${firstName}/schema`,
    );
    expect(schemaRes.ok()).toBeTruthy();

    const defaultsRes = await request.get(
      `${STACKKITS_API_URL}/api/v1/stackkits/${firstName}/defaults`,
    );
    expect(defaultsRes.ok()).toBeTruthy();
  });

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // Phase 7: Stack UI Pages (Post-Deployment)
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  test("31 - Dashboard with deployed stack screenshot", async () => {
    await sharedPage.goto("/stacks");
    await waitForPageLoad(sharedPage);

    await captureEvidenceScreenshot(
      sharedPage,
      "orchestration-21-dashboard-deployed.png",
      { fullPage: true, maxDiffPixelRatio: 0.1 },
    );
  });

  test("33 - Services page screenshot", async () => {
    await sharedPage.goto("/services");
    await waitForPageLoad(sharedPage);

    await captureEvidenceScreenshot(
      sharedPage,
      "orchestration-23-services.png",
      {
        fullPage: true,
        maxDiffPixelRatio: 0.1,
      },
    );
  });

  test("35 - Monitoring page screenshot", async () => {
    await sharedPage.goto("/monitoring");
    await waitForPageLoad(sharedPage);

    await captureEvidenceScreenshot(
      sharedPage,
      "orchestration-25-monitoring.png",
      { fullPage: true, maxDiffPixelRatio: 0.1 },
    );
  });

  test("36 - OTLP monitoring health proof via API", async ({ request }) => {
    test.skip(!authToken, "No auth token");

    const res = await request.get(`${API_BASE}/api/v1/monitor/health`, {
      headers: { Authorization: `Bearer ${authToken}` },
    });
    expect(res.ok()).toBeTruthy();

    const json = await res.json();
    const health = json.data ?? json;
    expect(health.ingestBackend).toBeTruthy();
    expect(health.collectorMode).toBeTruthy();
    expect(health.compatibilityMode).toBeTruthy();
    expect(health.otlp).toBeTruthy();
    expect(health.otlp.status).toBeTruthy();
    expect(health.legacyPush).toBeTruthy();
  });

  test("38 - Settings page screenshot", async () => {
    await sharedPage.goto("/settings");
    await waitForPageLoad(sharedPage);

    await expect(sharedPage.getByText(TEST_CREDENTIALS.email)).toBeVisible();

    await captureEvidenceScreenshot(
      sharedPage,
      "orchestration-28-settings.png",
      {
        fullPage: true,
        maxDiffPixelRatio: 0.1,
      },
    );
  });

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // Phase 8: Combined Summary Screenshot
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  test("39 - Full orchestration summary screenshot", async () => {
    const containers = execInVM(
      "docker ps --format '{{.Names}}\\t{{.Status}}'",
    );
    const stats = execInVM(
      "docker stats --no-stream --format '{{.Name}}\\t{{.CPUPerc}}\\t{{.MemUsage}}'",
    );
    const images = execInVM(
      "docker images --format '{{.Repository}}:{{.Tag}}'",
    );
    const volumes = execInVM("docker volume ls --format '{{.Name}}'");
    const networks = execInVM("docker network ls --format '{{.Name}}'");
    const loadAvg = execInVM("cat /proc/loadavg").trim();
    const mem = execInVM("free -h | grep Mem").trim();

    const page = await ctx.newPage();
    await page.setContent(`
        <html><body style="background:#0d1117;color:#c9d1d9;font-family:monospace;padding:24px;min-width:1400px">
          <h2 style="color:#58a6ff;margin-bottom:4px">Orchestration Summary</h2>
          <p style="color:#8b949e;margin-bottom:16px">Load: ${escapeHtml(loadAvg)} | ${escapeHtml(mem)}</p>
          <div style="display:grid;grid-template-columns:1fr 1fr;gap:16px">
            <div>
              <h3 style="color:#8b949e;margin-bottom:8px">Running Containers</h3>
              <pre style="background:#161b22;padding:12px;border-radius:8px;font-size:11px;line-height:1.4">${escapeHtml(containers)}</pre>
              <h3 style="color:#8b949e;margin-top:12px;margin-bottom:8px">Resource Usage</h3>
              <pre style="background:#161b22;padding:12px;border-radius:8px;font-size:11px;line-height:1.4">${escapeHtml(stats)}</pre>
            </div>
            <div>
              <h3 style="color:#8b949e;margin-bottom:8px">Images</h3>
              <pre style="background:#161b22;padding:12px;border-radius:8px;font-size:11px;line-height:1.4">${escapeHtml(images)}</pre>
              <h3 style="color:#8b949e;margin-top:12px;margin-bottom:8px">Volumes</h3>
              <pre style="background:#161b22;padding:12px;border-radius:8px;font-size:11px;line-height:1.4">${escapeHtml(volumes)}</pre>
              <h3 style="color:#8b949e;margin-top:12px;margin-bottom:8px">Networks</h3>
              <pre style="background:#161b22;padding:12px;border-radius:8px;font-size:11px;line-height:1.4">${escapeHtml(networks)}</pre>
            </div>
          </div>
        </body></html>
      `);

    await captureEvidenceScreenshot(page, "orchestration-29-summary.png", {
      fullPage: true,
    });
    await page.close();
  });
});
