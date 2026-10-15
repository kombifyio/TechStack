import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { type Route } from "@playwright/test";
import { test, expect } from "./fixtures";
import { requireAppBase } from "../helpers/test-utils";

// Owner direction 2026-10-10 (entity-cards concept §5): one Actors page for
// everyone who acts, people and agents, with the Offices and Factories the
// agents work in. A Factory opens the detail sheet with its own agents; an
// archived Office is no workplace anymore.

const sessionCookie = "techstack_session=embedded-session";

let portalServer: Server;
let portalOrigin: string;

test.beforeAll(async () => {
  portalServer = createServer((req, res) => {
    const child = new URL(req.url ?? "/", "http://127.0.0.1").searchParams.get(
      "child",
    );
    res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
    // The kombify Cloud tool frame: same sandbox, the portal's answers to the
    // frame's token requests, and a log of what the frame tells the host.
    res.end(`<!doctype html><html><body>
<iframe id="techstack-frame" src="${(child ?? "").replace(/&/g, "&amp;").replace(/"/g, "&quot;")}"
  sandbox="allow-same-origin allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-storage-access-by-user-activation"
  style="width:1200px;height:900px;border:0"></iframe>
<script>
  window.hostLog = [];
  window.askForViewContext = () => {
    const frame = document.getElementById("techstack-frame");
    frame.contentWindow.postMessage(
      { type: "view-context-request" },
      new URL(frame.src).origin,
    );
  };
  window.addEventListener("message", (event) => {
    const type = event.data && event.data.type;
    if (type === "view-context" || type === "ai-handover") {
      window.hostLog.push(event.data);
    }
    if (type === "auth-request") {
      event.source.postMessage({ type: "auth-token", token: "portal-token" }, event.origin);
    }
    if (type === "gateway-token-request") {
      event.source.postMessage({
        type: "gateway-token",
        token: "gateway-token",
        audience: "https://api.kombify.io",
        expiresAt: Date.now() + 60000
      }, event.origin);
    }
  });
</script>
</body></html>`);
  });
  await new Promise<void>((resolve, reject) => {
    portalServer.once("error", reject);
    portalServer.listen(0, "127.0.0.1", () => resolve());
  });
  portalOrigin = `http://127.0.0.1:${(portalServer.address() as AddressInfo).port}`;
});

test.afterAll(async () => {
  await new Promise<void>((resolve, reject) => {
    portalServer.close((err) => (err ? reject(err) : resolve()));
  });
});

function apiEnvelope(data: unknown) {
  return {
    data,
    meta: { request_id: "test", timestamp: new Date().toISOString() },
  };
}

async function fulfillJson(
  route: Route,
  data: unknown,
  status = 200,
  headers: Record<string, string> = {},
) {
  await route.fulfill({
    status,
    contentType: "application/json",
    // The agent catalogue is cross-origin (api.kombify.io).
    headers: { "Access-Control-Allow-Origin": "*", ...headers },
    body: JSON.stringify(data),
  });
}

// A Cloud-hosted Techstack with one homelab, its household and the agent catalogue.
async function cloudBackend(route: Route) {
  const request = route.request();
  const url = new URL(request.url());
  const path =
    url.origin === "https://api.kombify.io" &&
    url.pathname.startsWith("/v1/techstack/")
      ? `/api/v1/${url.pathname.slice("/v1/techstack/".length)}`
      : url.pathname;
  if (path.startsWith("/v1/ai/") && request.method() === "OPTIONS") {
    await route.fulfill({
      status: 204,
      headers: {
        "Access-Control-Allow-Origin": "*",
        "Access-Control-Allow-Headers": "authorization, accept",
        "Access-Control-Allow-Methods": "GET",
      },
    });
    return;
  }
  if (
    !(path.startsWith("/api/") || path.startsWith("/v1/ai/")) ||
    request.method() === "OPTIONS"
  ) {
    await route.fallback();
    return;
  }
  switch (`${request.method()} ${path}`) {
    case "GET /api/v1/client/bootstrap":
      return fulfillJson(
        route,
        apiEnvelope({
          edition: "saas",
          deployment_mode: "saas",
          kombify_edition: "cloud",
          version: "test",
          public_origin: "",
          telemetry: {
            sentry: { dsn: "", environment: "", release: "" },
            posthog: { key: "", host: "", environment: "" },
          },
        }),
      );
    case "GET /api/v1/auth/mode":
      return fulfillJson(
        route,
        apiEnvelope({
          mode: "cloud",
          deployment_mode: "saas",
          is_first_run: false,
          cloud_auth_url: "/api/v2/auth/login",
          portal_url: portalOrigin,
          allow_local_login: false,
        }),
      );
    case "GET /api/v2/auth/providers":
      return fulfillJson(route, {
        providers: [
          { id: "auth0", kind: "auth0", issuer: "https://login.kombify.io/" },
        ],
      });
    case "GET /api/v2/whoami":
      if (!request.headers().cookie?.includes(sessionCookie)) {
        return fulfillJson(route, { error: "missing session token" }, 401);
      }
      return fulfillJson(route, {
        subject: "auth0|user",
        tenantId: "tenant-1",
        email: "portal-user@example.test",
        provider: "cloud",
        role: "owner",
      });
    case "POST /api/v1/auth/portal-verify":
      return fulfillJson(
        route,
        apiEnvelope({
          cloud_user: {
            sub: "auth0|user",
            email: "portal-user@example.test",
            name: "Portal User",
            is_admin: true,
          },
        }),
        200,
        { "Set-Cookie": `${sessionCookie}; Path=/; HttpOnly; SameSite=Lax` },
      );
    case "GET /api/v1/csrf":
      return fulfillJson(route, { token: "csrf" }, 200, {
        "X-CSRF-Token": "csrf",
      });
    case "GET /api/v1/features":
      return fulfillJson(
        route,
        apiEnvelope({ security: [], beta: [], ux: [] }),
      );
    case "GET /api/v1/homelab":
      return fulfillJson(
        route,
        apiEnvelope({ homelab: null, kit_deployments: [{ id: "stack-1" }] }),
      );
    case "GET /api/v1/stacks/stack-1/identity":
      return fulfillJson(
        route,
        apiEnvelope({
          owner: { status: "active", origin: "https://id.home.example" },
          household: {
            members: [
              {
                username: "lena",
                display_name: "Lena",
                email: "lena@home.example",
                status: "Member",
              },
            ],
          },
        }),
      );
    case "GET /v1/ai/agents":
      return fulfillJson(route, {
        agents: [
          {
            // The live catalogue names no harness for most agents.
            id: "kombify-assistant",
            displayName: "kombify AI",
            description: "Platform-wide agent for knowledge and tools.",
            group: "ai",
            agentTier: "free",
            executionStatus: "available",
          },
          {
            id: "homelab-orchestrator",
            displayName: "Homelab Lead",
            description: "Keeps the homelab running",
            group: "server",
            agentTier: "premium",
            executionStatus: "available",
            harness: { id: "homelab", label: "Homelab", role: "orchestrator" },
          },
          {
            id: "backup-expert",
            displayName: "Backup Expert",
            group: "server",
            executionStatus: "available",
            harness: { id: "homelab", label: "Homelab", role: "expert" },
          },
          {
            id: "mail-expert",
            displayName: "Mail Assistant",
            executionStatus: "disabled",
            harness: { id: "paperwork", label: "Paperwork", role: "expert" },
          },
        ],
      });
    case "GET /v1/ai/offices":
      return fulfillJson(route, {
        offices: [
          {
            id: "office-node",
            factoryId: "homelab",
            name: "Basement server",
            contextKey: "node-01.home",
          },
          {
            id: "office-old",
            factoryId: "homelab",
            name: "Retired",
            archivedAt: "2026-10-01T00:00:00Z",
          },
          {
            id: "office-mail",
            factoryId: "paperwork",
            name: "Family mail",
            contextKey: "family@home.example",
          },
        ],
      });
    default:
      return fulfillJson(route, { error: "unmocked" }, 404);
  }
}

test("actors lists people, agents and their workplaces, and a Factory opens its agents", async ({
  context,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  await context.route("**/*", cloudBackend);
  const page = await context.newPage();
  const child = `${origin}/dashboard?embedded=true`;
  await page.goto(`${portalOrigin}/?child=${encodeURIComponent(child)}`);
  const app = page.frameLocator("#techstack-frame");
  await app
    .getByRole("link", { name: "Actors" })
    .first()
    .click({ timeout: 20_000 });

  const people = app.getByRole("region", { name: "People" });
  await expect(people.getByText("Lena", { exact: true })).toBeVisible({
    timeout: 20_000,
  });
  const agents = app.getByRole("region", { name: "Agents" }).first();
  await expect(agents.getByText("Homelab Lead", { exact: true })).toBeVisible();
  await expect(agents.getByText("Keeps the homelab running")).toBeVisible();
  const places = app
    .getByRole("region", { name: "Offices and Factories" })
    .first();
  await expect(
    places.getByText("Basement server", { exact: true }),
  ).toBeVisible();
  await expect(places.getByText("Retired")).toHaveCount(0);

  await places.getByRole("button", { name: /^Homelab/ }).click();
  const sheet = app.getByTestId("actors-workplace-agents");
  await expect(sheet.getByText("Backup Expert", { exact: true })).toBeVisible();
  await expect(sheet.getByText("Mail Assistant")).toHaveCount(0);
});
