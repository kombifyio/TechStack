import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { type FrameLocator, type Page, type Route } from "@playwright/test";
import { test, expect } from "./fixtures";
import { requireAppBase } from "../helpers/test-utils";

// Opening a terminal needs a fresh sign-in. Inside kombify Cloud the app runs
// in a frame, and Auth0 refuses to be framed: navigating the frame to the
// sign-in killed the whole embedded page (owner report 2026-10-07). The
// confirmation has to happen in a separate window, after which the frame
// starts the terminal session again. Guard changes and credential reveals
// need the same confirmation and follow the same rule; a denial that no
// sign-in resolves (the demo account) must never start one.

const serverId = "srv-embedded";
const deploymentId = "stack-embedded";
const sessionCookie = "techstack_session=embedded-session";

let portalServer: Server;
let portalOrigin: string;

test.beforeAll(async () => {
  portalServer = createServer((req, res) => {
    const child = new URL(req.url ?? "/", "http://127.0.0.1").searchParams.get(
      "child",
    );
    res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
    // The kombify Cloud tool frame: same sandbox (no top navigation, no
    // modals) and the portal's answers to the frame's token requests.
    res.end(`<!doctype html><html><body>
<iframe id="techstack-frame" src="${(child ?? "").replace(/&/g, "&amp;").replace(/"/g, "&quot;")}"
  sandbox="allow-same-origin allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-storage-access-by-user-activation"
  style="width:1200px;height:700px;border:0"></iframe>
<script>
  window.addEventListener("message", (event) => {
    const type = event.data && event.data.type;
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
    headers,
    body: JSON.stringify(data),
  });
}

const deployment = {
  id: deploymentId,
  name: "basement",
  mode: "easy",
  status: "running",
  state: "running",
  provider: "local",
  services: [],
  created_at: "2026-10-07T09:00:00Z",
  updated_at: "2026-10-07T09:00:00Z",
};

// A Cloud-hosted Techstack with one connected server, answering as the backend
// and the gateway do. The terminal-session route is the test's own, and so is
// every route in `extra` (keyed "METHOD /path").
function cloudBackend(
  terminalSessions: (route: Route) => Promise<void>,
  extra: Record<string, (route: Route) => Promise<void>> = {},
) {
  return async (route: Route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path =
      url.origin === "https://api.kombify.io" &&
      url.pathname.startsWith("/v1/techstack/")
        ? `/api/v1/${url.pathname.slice("/v1/techstack/".length)}`
        : url.pathname;
    if (!path.startsWith("/api/") || request.method() === "OPTIONS") {
      await route.fallback();
      return;
    }
    const key = `${request.method()} ${path}`;
    switch (key) {
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
      case "GET /api/v1/stacks":
        return fulfillJson(route, apiEnvelope([deployment]));
      case "GET /api/v1/homelab":
        return fulfillJson(
          route,
          apiEnvelope({
            homelab: {
              id: "hl-1",
              name: "My Homelab",
              named: true,
              intent: {},
            },
            kit_deployments: [deployment],
          }),
        );
      case "GET /api/v1/workers":
      case "GET /api/v1/services":
        return fulfillJson(route, apiEnvelope([]));
      case "GET /api/v1/servers":
        return fulfillJson(
          route,
          apiEnvelope([
            {
              id: serverId,
              node_id: "node-embedded",
              kit_deployment_id: deploymentId,
              name: "embedded-box",
              lifecycle: { state: "active", desired_state: "active" },
              connection: {
                state: "connected",
                changed_at: new Date().toISOString(),
              },
              health: { state: "healthy" },
              channels: [],
              inventory_revision: 1,
              provider: {},
              mutations_allowed: true,
            },
          ]),
        );
      case `GET /api/v1/stacks/${deploymentId}/operations`:
        return fulfillJson(
          route,
          apiEnvelope({
            stack: {
              id: deploymentId,
              name: "basement",
              status: "running",
              state: "running",
            },
            readiness: {
              status: "ready",
              can_start: false,
              required_servers: 1,
              approved_servers: 1,
              connected_servers: 1,
            },
            nextSteps: [],
            kpis: {
              registered_servers: 1,
              healthy_servers: 1,
              running_services: 0,
            },
            servers: [],
            services: [],
            monitoring: { status: "healthy", alerts: [] },
            alerts: [],
          }),
        );
      case `GET /api/v1/servers/${serverId}/access`:
        return fulfillJson(
          route,
          apiEnvelope({
            server_id: serverId,
            terminal_enabled: true,
            manual_command_available: true,
            connect_ready: true,
            ssh_command: "ssh kombify@203.0.113.7",
          }),
        );
      case `POST /api/v1/servers/${serverId}/terminal-sessions`:
        return terminalSessions(route);
      default:
        if (extra[key]) return extra[key](route);
        if (path.startsWith("/api/v1/jobs")) {
          return fulfillJson(route, apiEnvelope({ items: [] }));
        }
        return fulfillJson(route, { error: `unmocked ${key}` }, 404);
    }
  };
}

test("an embedded terminal step-up leaves the frame in place and starts the session after confirmation", async ({
  context,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);

  let sessionRequests = 0;
  await context.route(
    "**/*",
    cloudBackend(async (route) => {
      sessionRequests += 1;
      if (sessionRequests === 1) {
        await fulfillJson(
          route,
          {
            error: {
              code: "FORBIDDEN",
              message: "Fresh server-terminal re-authentication required",
            },
          },
          403,
        );
        return;
      }
      await fulfillJson(
        route,
        apiEnvelope({
          session_id: "session-1",
          stream_url: `ws://127.0.0.1:${new URL(origin).port}/api/v1/terminal-sessions/session-1/stream`,
          expires_at: new Date(Date.now() + 600_000).toISOString(),
          idle_limit_seconds: 300,
        }),
        201,
      );
    }),
  );
  await context.routeWebSocket(/terminal-sessions\/session-1\/stream/, (ws) => {
    ws.send(JSON.stringify({ type: "status", data: "connected" }));
  });

  // The step-up window: the sign-in lands on the page the frame asked for.
  const stepUpLogins: URL[] = [];
  await context.route("**/api/v2/auth/login**", async (route) => {
    const url = new URL(route.request().url());
    stepUpLogins.push(url);
    await route.fulfill({
      status: 302,
      headers: { location: url.searchParams.get("return_to") ?? "/" },
    });
  });

  const page = await context.newPage();
  const frameLogins: string[] = [];
  page.on("request", (request) => {
    if (
      request.url().includes("/api/v2/auth/login") &&
      request.frame() !== page.mainFrame()
    ) {
      frameLogins.push(request.url());
    }
  });

  const child = `${origin}/dashboard?embedded=true&host_navigation=true`;
  await page.goto(`${portalOrigin}/?child=${encodeURIComponent(child)}`);
  const app = page.frameLocator("#techstack-frame");

  // Node actions live in the card's menu, which opens above the list.
  await app.getByTestId("node-actions-toggle").click();
  await app.getByRole("button", { name: "Open terminal" }).click();
  const prompt = app.getByTestId("step-up-prompt");
  await expect(prompt).toBeVisible();
  expect(sessionRequests).toBe(1);

  const popupOpened = page.waitForEvent("popup");
  await app.getByTestId("step-up-confirm").click();
  const popup = await popupOpened;

  // The completion signal made the frame start the session once more.
  await expect(prompt).toBeHidden({ timeout: 15_000 });
  await expect(app.getByRole("dialog")).toContainText("connected");
  expect(sessionRequests).toBe(2);

  // The sign-in was requested for this server, in the other window only, and
  // that window closed itself.
  expect(
    stepUpLogins.map((url) => url.searchParams.get("reauth_resource")),
  ).toEqual([serverId]);
  expect(frameLogins).toEqual([]);
  await expect.poll(() => popup.isClosed()).toBe(true);
});

// Opens a page inside the Cloud frame the way the portal does: the frame
// loads the dashboard, whose portal sign-in establishes the session, and the
// portal then navigates it. A direct deep link would reach the page before
// that session exists.
async function openInFrame(page: Page, origin: string, path: string) {
  const child = `${origin}/dashboard?embedded=true&host_navigation=true`;
  await page.goto(`${portalOrigin}/?child=${encodeURIComponent(child)}`);
  const app = page.frameLocator("#techstack-frame");
  await expect(
    // The dashboard also renders before the portal session is confirmed.
    // Its authenticated main content appears after that exchange completes.
    app.getByRole("main").getByTestId("node-actions-toggle"),
  ).toBeVisible();
  await page.evaluate((target) => {
    const frame = document.querySelector<HTMLIFrameElement>("#techstack-frame");
    frame?.contentWindow?.postMessage({ type: "navigate", path: target }, "*");
  }, path);
  const frame = page
    .frames()
    .find((frame) => frame.url().startsWith(`${origin}/`));
  if (!frame) throw new Error("The Techstack frame did not load");
  const target = new URL(path, origin);
  await frame.waitForURL(
    (url) => url.pathname === target.pathname && url.hash === target.hash,
  );
  return app;
}

// A structured denial is not a re-authentication refusal: a fresh sign-in
// cannot lift the demo restriction, so the owner is told about it and no
// sign-in starts, embedded or standalone.
const demoGuidance =
  "This is the shared kombify live demo. Destructive and shell-level actions are disabled here.";

for (const placement of ["embedded", "standalone"] as const) {
  test(`a demo-account restriction on the terminal is shown instead of a sign-in (${placement})`, async ({
    context,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    let sessionRequests = 0;
    await context.route(
      "**/*",
      cloudBackend(async (route) => {
        sessionRequests += 1;
        await fulfillJson(
          route,
          {
            error: {
              code: "FORBIDDEN",
              message: "This action is disabled on the kombify demo account",
              details: {
                error_code: "demo_account_restricted",
                reason_code: "demo_account",
                retryable: false,
                user_guidance: { title: "Disabled", body: demoGuidance },
              },
            },
          },
          403,
        );
      }),
    );
    const logins: string[] = [];
    await context.route("**/api/v2/auth/login**", async (route) => {
      logins.push(route.request().url());
      await route.fulfill({ status: 302, headers: { location: "/" } });
    });

    const page = await context.newPage();
    let app: Page | FrameLocator = page;
    if (placement === "embedded") {
      const child = `${origin}/dashboard?embedded=true&host_navigation=true`;
      await page.goto(`${portalOrigin}/?child=${encodeURIComponent(child)}`);
      app = page.frameLocator("#techstack-frame");
    } else {
      await context.addCookies([
        { name: "techstack_session", value: "embedded-session", url: origin },
      ]);
      await page.goto(`${origin}/dashboard`);
    }

    // Node actions live in the card's menu, which opens above the list.
    await app.getByTestId("node-actions-toggle").click();
    await app.getByRole("button", { name: "Open terminal" }).click();
    await expect(app.getByRole("dialog")).toContainText(demoGuidance);
    await expect(app.getByTestId("step-up-prompt")).toHaveCount(0);
    expect(sessionRequests).toBe(1);
    expect(logins).toEqual([]);
  });
}

// Changing how a Guard server is reached needs a fresh sign-in. The frame
// confirms it in a separate window and then only clears the notice: the
// change itself is sensitive and is repeated by the owner, never by the page.
const guardServer = {
  id: serverId,
  node_id: "node-embedded",
  kit_deployment_id: deploymentId,
  name: "embedded-box",
  worker_id: "worker-embedded",
  lifecycle: { state: "active", desired_state: "active" },
  connection: { state: "connected", changed_at: "2026-10-07T09:00:00Z" },
  health: { state: "healthy" },
  channels: [],
  inventory_revision: 1,
  provider: {},
  mutations_allowed: true,
  reach: {
    state: "ready",
    public: { enabled: false },
    kombify_me: { state: "registered", address: "home.kombify.me" },
    connect: { state: "registered", node_id: "node-embedded" },
    relay: { state: "connected" },
    updated_at: "2026-10-07T09:00:00Z",
  },
  created_at: "2026-10-07T09:00:00Z",
  updated_at: "2026-10-07T09:00:00Z",
};

test("an embedded Guard change that needs a fresh sign-in confirms in a separate window and waits for the owner to repeat it", async ({
  context,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);

  let reachWrites = 0;
  await context.route(
    "**/*",
    cloudBackend(async (route) => fulfillJson(route, {}, 404), {
      [`GET /api/v1/servers/${serverId}`]: (route) =>
        fulfillJson(route, apiEnvelope(guardServer)),
      [`GET /api/v1/stacks/${deploymentId}/servers/${serverId}`]: (route) =>
        fulfillJson(
          route,
          apiEnvelope({
            stack: deployment,
            server: {
              id: serverId,
              hostname: "embedded-box",
              role: "foundation",
              status: "healthy",
              assignment: "stack",
              agent_id: "agent-embedded",
              approved: true,
              precheck_state: "passed",
              capabilities: {},
              health: { state: "healthy" },
            },
            services: [],
            checks: [],
            logs: [],
            health: { state: "healthy" },
            monitoring: {
              status: "ok",
              queryBackend: "prometheus",
              ingestBackend: "otlp",
              collectorMode: "gateway",
              compatibilityMode: "native",
            },
          }),
        ),
      [`PUT /api/v1/servers/${serverId}/reach`]: (route) => {
        reachWrites += 1;
        return fulfillJson(
          route,
          {
            error: {
              code: "FORBIDDEN",
              message:
                "A fresh sign-in is required to change how this server is reached",
              details: {
                reason: "server_reach_step_up_required",
                retryable: true,
              },
            },
          },
          403,
        );
      },
    }),
  );
  const stepUpLogins: URL[] = [];
  await context.route("**/api/v2/auth/login**", async (route) => {
    const url = new URL(route.request().url());
    stepUpLogins.push(url);
    await route.fulfill({
      status: 302,
      headers: { location: url.searchParams.get("return_to") ?? "/" },
    });
  });

  const page = await context.newPage();
  const frameLogins: string[] = [];
  page.on("request", (request) => {
    if (
      request.url().includes("/api/v2/auth/login") &&
      request.frame() !== page.mainFrame()
    ) {
      frameLogins.push(request.url());
    }
  });
  const app = await openInFrame(
    page,
    origin,
    `/stacks/${deploymentId}/servers/${serverId}`,
  );

  await app.getByTestId("guard-reach-service").fill("web");
  await app.getByTestId("guard-reach-port").fill("8080");
  await app.getByTestId("guard-reach-enable").click();
  const notice = app.getByTestId("guard-reach-stepup");
  await expect(notice).toBeVisible();
  expect(reachWrites).toBe(1);

  const popupOpened = page.waitForEvent("popup");
  await notice.getByTestId("step-up-confirm").click();
  const popup = await popupOpened;

  // The completion signal clears the notice. The change is not sent again.
  await expect(notice).toBeHidden({ timeout: 15_000 });
  expect(reachWrites).toBe(1);

  // A plain fresh sign-in, in the other window only, back to the completion page.
  expect(stepUpLogins).toHaveLength(1);
  expect(stepUpLogins[0].searchParams.get("prompt")).toBe("login");
  expect(stepUpLogins[0].searchParams.get("max_age")).toBe("0");
  expect(stepUpLogins[0].searchParams.get("return_to")).toBe(
    "/auth/reauth-complete",
  );
  expect(stepUpLogins[0].searchParams.has("reauth_resource")).toBe(false);
  expect(frameLogins).toEqual([]);
  await expect.poll(() => popup.isClosed()).toBe(true);
});

// Revealing a credential in the hosted deployment needs a fresh sign-in bound
// to that item. The frame confirms it in a separate window; the confirmation
// reveals the item once and the owner's next action finds it revealed.
test("an embedded credential reveal confirms the sign-in in a separate window and then reveals once", async ({
  context,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  const itemId = "item-recovery";
  const secret = "correct-horse-battery";

  let proofRequests = 0;
  let revealRequests = 0;
  await context.route(
    "**/*",
    cloudBackend(async (route) => fulfillJson(route, {}, 404), {
      "GET /api/v1/wallet": (route) =>
        fulfillJson(
          route,
          apiEnvelope([
            {
              id: itemId,
              name: "Recovery code",
              kind: "password",
              item_class: "recovery",
              has_secret: true,
              revealable: true,
            },
          ]),
        ),
      [`POST /api/v1/wallet/${itemId}/reauth-proof`]: (route) => {
        proofRequests += 1;
        if (proofRequests === 1) {
          return fulfillJson(
            route,
            {
              error: {
                code: "FORBIDDEN",
                message: "Fresh platform re-authentication required",
                details: {
                  reason: "wallet_reveal_step_up_required",
                  retryable: true,
                },
              },
            },
            403,
          );
        }
        return fulfillJson(
          route,
          apiEnvelope({
            id: itemId,
            reauth_timestamp: String(Math.floor(Date.now() / 1000)),
            reauth_signature: "proof",
            expires_at: new Date(Date.now() + 300_000).toISOString(),
          }),
        );
      },
      [`POST /api/v1/wallet/${itemId}/reveal`]: (route) => {
        revealRequests += 1;
        return fulfillJson(route, apiEnvelope({ id: itemId, secret }));
      },
    }),
  );
  const stepUpLogins: URL[] = [];
  await context.route("**/api/v2/auth/login**", async (route) => {
    const url = new URL(route.request().url());
    stepUpLogins.push(url);
    await route.fulfill({
      status: 302,
      headers: { location: url.searchParams.get("return_to") ?? "/" },
    });
  });

  const page = await context.newPage();
  const frameLogins: string[] = [];
  page.on("request", (request) => {
    if (
      request.url().includes("/api/v2/auth/login") &&
      request.frame() !== page.mainFrame()
    ) {
      frameLogins.push(request.url());
    }
  });
  const app = await openInFrame(page, origin, "/wallet#recovery");

  await app.getByRole("button", { name: "Show", exact: true }).click();
  const prompt = app.getByTestId("step-up-prompt");
  await expect(prompt).toBeVisible();
  expect(proofRequests).toBe(1);

  const popupOpened = page.waitForEvent("popup");
  await app.getByTestId("step-up-confirm").click();
  const popup = await popupOpened;

  // The completion signal reveals the item once; the prompt goes away.
  await expect(prompt).toBeHidden({ timeout: 15_000 });
  expect(proofRequests).toBe(2);
  expect(revealRequests).toBe(1);

  // The sign-in was bound to this item, in the other window only.
  expect(stepUpLogins).toHaveLength(1);
  expect(stepUpLogins[0].searchParams.get("reauth_resource")).toBe(itemId);
  expect(stepUpLogins[0].searchParams.get("reauth_purpose")).toBe(
    "kombify.cloud.wallet.reauth.v1",
  );
  expect(stepUpLogins[0].searchParams.get("prompt")).toBe("login");
  expect(frameLogins).toEqual([]);
  await expect.poll(() => popup.isClosed()).toBe(true);

  // The owner's own action now shows the revealed item without asking again.
  await app.getByRole("button", { name: "Show", exact: true }).click();
  await expect(app.getByText(secret)).toBeVisible();
  expect(proofRequests).toBe(2);
  expect(revealRequests).toBe(1);
});
