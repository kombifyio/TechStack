import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { createServer } from "node:http";
import path from "node:path";
import { fileURLToPath } from "node:url";

const rootDir = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
);

const mockBackendURL =
  process.env.PLAYWRIGHT_MOCK_BACKEND_URL ?? "http://127.0.0.1:5276";
const mockBackendOrigin = new URL(mockBackendURL);
const frontendURL = process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:5261";
const frontendOrigin = new URL(frontendURL);
const frontendHost = frontendOrigin.hostname;
const frontendPort = frontendOrigin.port || "5261";
const frontendMode = process.env.PLAYWRIGHT_NO_SETUP_SERVER_MODE ?? "vite";

if (frontendMode !== "vite" && frontendMode !== "build") {
  throw new Error(
    `Unsupported PLAYWRIGHT_NO_SETUP_SERVER_MODE ${JSON.stringify(frontendMode)}; expected vite or build`,
  );
}

const env = {
  ...process.env,
  TECHSTACK_API_URL: process.env.TECHSTACK_API_URL ?? mockBackendURL,
  POCKETBASE_URL: process.env.POCKETBASE_URL ?? mockBackendURL,
  VITE_API_URL: process.env.VITE_API_URL ?? mockBackendURL,
};

function sendJSON(res, status, body) {
  res.statusCode = status;
  res.setHeader("Content-Type", "application/json");
  res.end(JSON.stringify(body));
}

function redirect(res, location) {
  res.statusCode = 302;
  res.setHeader("Location", location);
  res.end();
}

function handlerKey(method, pathname) {
  return `${method.toUpperCase()} ${pathname}`;
}

const handlers = new Map([
  [
    handlerKey("GET", "/api/v1/client/bootstrap"),
    (_req, res) =>
      sendJSON(res, 200, {
        data: {
          edition: "selfhost-oss",
          deployment_mode: "self-hosted",
          kombify_edition: "",
          version: "0.0.0-test",
          public_origin: "",
          telemetry: {
            sentry: { dsn: "", environment: "", release: "" },
            posthog: { key: "", host: "", environment: "" },
          },
        },
      }),
  ],
  // Root-path auth redirects owned by the Go backend since the static-SPA
  // convergence (internal/routes/auth/web_redirects.go); mirrored here so the
  // browser flows behave like production.
  [
    handlerKey("GET", "/auth/callback"),
    (_req, res, url) => redirect(res, `/api/v2/auth/callback${url.search}`),
  ],
  [
    handlerKey("GET", "/auth/logout"),
    (req, res) => {
      const cookies = req.headers.cookie ?? "";
      const hasSession = /(?:^|;\s*)techstack_session=[^;]+/.test(cookies);
      redirect(
        res,
        hasSession
          ? `/api/v2/auth/logout?next=${encodeURIComponent("/login?manual=1&logged_out=1")}`
          : "/api/v1/auth/logout",
      );
    },
  ],
  [
    handlerKey("GET", "/api/v1/auth/mode"),
    (_req, res) =>
      sendJSON(res, 200, {
        data: {
          mode: "local",
          deployment_mode: "self-hosted",
          is_first_run: false,
          cloud_auth_url: null,
          portal_url: null,
          allow_local_login: true,
        },
      }),
  ],
  [
    handlerKey("GET", "/api/v1/auth/methods"),
    (_req, res) =>
      sendJSON(res, 200, {
        providers: [
          {
            id: "primary",
            kind: "auth0",
            label: "kombify Cloud",
            auth_url: "/api/v2/auth/login",
          },
        ],
        breakglass: {
          initialized: true,
          claimed: false,
          email: "breakglass@techstack.local",
          has_pending_reveal: false,
          reveal_expires_at: null,
          locked: false,
        },
      }),
  ],
  [
    handlerKey("GET", "/api/v1/info"),
    (_req, res) => sendJSON(res, 200, { data: { version: "test" } }),
  ],
  [
    handlerKey("GET", "/api/v1/registry/services"),
    (_req, res) =>
      sendJSON(res, 200, {
        data: {
          catalog: [
            {
              id: "pocket_id",
              display_name: "Pocket ID",
              type: "auth",
              description:
                "Identity head for StackKit login and owner activation.",
              required: true,
              foundations: ["base-kit"],
            },
            {
              id: "vaultwarden",
              display_name: "Vaultwarden",
              type: "auth",
              description: "Password vault managed by the StackKit gateway.",
              recommended: true,
              foundations: ["base-kit"],
            },
          ],
          stacks: [
            {
              id: "stack-1",
              name: "Demo Stack",
              status: "running",
              stackkit_foundation: "base-kit",
            },
          ],
          servers: [
            {
              id: "node-1",
              stack_id: "stack-1",
              name: "foundation-1",
              hostname: "foundation-1",
              role: "foundation",
              role_label: "Foundation Node",
              rollout_ready: true,
            },
          ],
          services: [
            {
              id: "service-1",
              name: "custom_dashboard",
              display_name: "Custom Dashboard",
              type: "custom",
              status: "observed",
              management_state: "observed",
              stack_id: "stack-1",
              stack_name: "Demo Stack",
              server_id: "node-1",
              server_name: "foundation-1",
              port: 8088,
            },
          ],
        },
      }),
  ],
  [
    handlerKey("GET", "/api/v1/jobs"),
    (_req, res) =>
      sendJSON(res, 200, {
        data: {
          items: [
            {
              id: "job-1",
              type: "provision",
              state: "completed",
              progress: 100,
              stack_id: "stack-1",
              current_step: "Completed",
              created: "2026-05-18T10:00:00Z",
              updated: "2026-05-18T10:10:00Z",
            },
          ],
          page: 1,
          per_page: 50,
          total_items: 1,
          total_pages: 1,
        },
      }),
  ],
  // Canonical server read model. The UI reads servers from here now; the
  // legacy /api/v1/registry/servers projection has no client left.
  [
    handlerKey("GET", "/api/v1/servers"),
    (_req, res) =>
      sendJSON(res, 200, {
        data: [
          {
            id: "node-1",
            stack_id: "stack-1",
            name: "foundation-1",
            worker_id: "agent-1",
            lifecycle: { state: "active", desired_state: "running" },
            connection: {
              state: "connected",
              changed_at: "2026-05-18T10:00:00Z",
              last_heartbeat_at: "2026-05-18T10:10:00Z",
              staleness_seconds: 5,
            },
            health: { state: "healthy", observed_at: "2026-05-18T10:10:00Z" },
            channels: [],
            inventory_revision: 1,
            provider: {},
            mutations_allowed: true,
            created_at: "2026-05-18T09:00:00Z",
            updated_at: "2026-05-18T10:10:00Z",
          },
        ],
      }),
  ],
  [
    handlerKey("POST", "/api/v1/registry/services/attach"),
    async (req, res) => {
      const body = await readJson(req);
      sendJSON(res, 200, {
        data: {
          service: {
            id: `service-${body.service_id || "catalog"}`,
            name: body.service_id || "catalog",
            display_name: body.service_id || "Catalog Service",
            type: "auth",
            status: "pending",
            management_state: "managed",
            stack_id: body.stack_id,
            stack_name: "Demo Stack",
            server_id: body.server_id,
            server_name: "foundation-1",
          },
        },
      });
    },
  ],
  [
    handlerKey("POST", "/api/v1/registry/services/import"),
    async (req, res) => {
      const body = await readJson(req);
      sendJSON(res, 200, {
        data: {
          service: {
            id: `observed-${body.name || "service"}`,
            name: body.name || "service",
            display_name: body.display_name || body.name || "Observed Service",
            type: body.type || "custom",
            status: "observed",
            management_state: "observed",
            stack_id: body.stack_id,
            stack_name: "Demo Stack",
            server_id: body.server_id,
            server_name: "foundation-1",
            port: body.port,
            url: body.url,
          },
        },
      });
    },
  ],
  [
    handlerKey("GET", "/api/v1/trust/status"),
    (_req, res) => sendJSON(res, 200, { data: { can_auto_auth: false } }),
  ],
  [
    handlerKey("POST", "/api/v1/trust/pairing-tokens"),
    async (req, res) => {
      await readJson(req);
      sendJSON(res, 200, {
        data: {
          token: "pair-token-123",
          expires_at: "2026-05-18T12:30:00Z",
        },
      });
    },
  ],
  [
    handlerKey("POST", "/api/v1/agent/binary/linux/x86_64"),
    (req, res) =>
      sendJSON(res, 401, {
        error: "invalid pairing token",
        received_content_type: req.headers["content-type"] ?? "",
      }),
  ],
  [
    handlerKey("GET", "/api/v2/auth/providers"),
    (_req, res) =>
      sendJSON(res, 200, {
        providers: [
          {
            id: "primary",
            kind: "generic",
            issuer: "https://id.example.com",
          },
        ],
      }),
  ],
  [
    handlerKey("GET", "/api/v2/whoami"),
    (_req, res) => sendJSON(res, 401, { error: "missing session token" }),
  ],
  [
    handlerKey("GET", "/api/v2/auth/login"),
    (_req, res) => redirect(res, "/login?manual=1&error=mocked_v2_login"),
  ],
  [
    handlerKey("GET", "/api/v2/auth/logout"),
    (_req, res, url) =>
      redirect(
        res,
        url.searchParams.get("next") ?? "/login?manual=1&logged_out=1",
      ),
  ],
]);

async function readJson(req) {
  const chunks = [];
  for await (const chunk of req) {
    chunks.push(chunk);
  }
  if (chunks.length === 0) return {};
  const raw = Buffer.concat(chunks).toString("utf8");
  if (!raw.trim()) return {};
  return JSON.parse(raw);
}

const mockBackendServer = createServer(async (req, res) => {
  const url = new URL(req.url ?? "/", mockBackendOrigin);
  const handler = handlers.get(handlerKey(req.method ?? "GET", url.pathname));
  if (!handler) {
    sendJSON(res, 404, { error: `unhandled mock path: ${url.pathname}` });
    return;
  }
  await handler(req, res, url);
});

await new Promise((resolve, reject) => {
  mockBackendServer.once("error", reject);
  mockBackendServer.listen(
    Number(mockBackendOrigin.port),
    mockBackendOrigin.hostname,
    resolve,
  );
});

function startFrontend() {
  if (frontendMode === "build") {
    // adapter-static build (ADR-033 OQ2): serve build-static with the SPA
    // fallback server; backend paths proxy to the mock backend origin.
    const staticIndex = path.join(rootDir, "build-static", "index.html");
    if (!existsSync(staticIndex)) {
      throw new Error(
        "No built frontend found at build-static/index.html; run pnpm build before using PLAYWRIGHT_NO_SETUP_SERVER_MODE=build",
      );
    }
    return spawn(
      process.execPath,
      [path.join(rootDir, "scripts", "serve-static.mjs")],
      {
        cwd: rootDir,
        env: {
          ...env,
          HOST: frontendHost,
          PORT: frontendPort,
        },
        stdio: "inherit",
      },
    );
  }

  const viteEntrypoint = path.join(
    rootDir,
    "node_modules",
    "vite",
    "bin",
    "vite.js",
  );
  if (!existsSync(viteEntrypoint)) {
    throw new Error(
      "Vite is not installed in app/node_modules; run pnpm install before starting the Playwright server",
    );
  }
  const viteArgs = [
    viteEntrypoint,
    "dev",
    "--host",
    frontendHost,
    "--port",
    frontendPort,
    "--strictPort",
  ];
  return spawn(process.execPath, viteArgs, {
    cwd: rootDir,
    env,
    stdio: "inherit",
  });
}

const child = startFrontend();

function forward(signal) {
  if (!child.killed) {
    child.kill(signal);
  }
  mockBackendServer.close();
}

process.on("SIGINT", () => forward("SIGINT"));
process.on("SIGTERM", () => forward("SIGTERM"));

child.on("exit", (code, signal) => {
  mockBackendServer.close();
  if (signal) {
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code ?? 0);
});
