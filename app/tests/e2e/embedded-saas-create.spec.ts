import { expect, test, type Page, type Route } from "@playwright/test";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";

type ApiCall = {
  method: string;
  path: string;
  headers: Record<string, string>;
  body: unknown;
};

function isAuth0Host(hostname: string): boolean {
  return hostname === "auth0.com" || hostname.endsWith(".auth0.com");
}

let portalServer: Server;
let portalOrigin: string;

test.beforeAll(async () => {
  portalServer = createServer((req, res) => {
    const url = new URL(req.url ?? "/", "http://127.0.0.1");
    const childSrc = url.searchParams.get("child") ?? "";
    res.writeHead(200, {
      "Content-Type": "text/html; charset=utf-8",
      "Content-Security-Policy":
        "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; frame-src http: https:",
    });
    res.end(`<!doctype html>
<html>
  <head><title>kombify portal test host</title></head>
  <body>
    <iframe id="techstack-frame" src="${escapeHtml(childSrc)}" style="width: 1200px; height: 900px; border: 0"></iframe>
    <script>
      window.portalMessages = [];
      window.addEventListener("message", (event) => {
        window.portalMessages.push({ origin: event.origin, data: event.data });
        if (event.data && event.data.type === "auth-request") {
          event.source.postMessage({ type: "auth-token", token: "portal-token-e2e" }, event.origin);
        }
        if (event.data && event.data.type === "gateway-token-request") {
          event.source.postMessage({
            type: "gateway-token",
            token: "gateway-token-e2e",
            audience: "https://api.kombify.io",
            expiresAt: Date.now() + 60000
          }, event.origin);
        }
      });
    </script>
  </body>
</html>`);
  });

  await new Promise<void>((resolve, reject) => {
    portalServer.once("error", reject);
    portalServer.listen(0, "127.0.0.1", () => resolve());
  });
  const address = portalServer.address() as AddressInfo;
  portalOrigin = `http://127.0.0.1:${address.port}`;
});

test.afterAll(async () => {
  await new Promise<void>((resolve, reject) => {
    portalServer.close((err) => (err ? reject(err) : resolve()));
  });
});

test.describe("Embedded SaaS create flow", () => {
  test("creates through parent SSO without opening Auth0 inside the iframe", async ({
    page,
    baseURL,
  }) => {
    if (!baseURL) throw new Error("PLAYWRIGHT_BASE_URL is required");

    const apiCalls: ApiCall[] = [];
    const forbiddenAuthNavigations: string[] = [];
    await installEmbeddedSaaSApi(page, apiCalls, forbiddenAuthNavigations);

    page.on("request", (request) => {
      const url = request.url();
      if (
        isAuth0Host(new URL(url).hostname) ||
        url.includes("/authorize") ||
        url.includes("/api/v2/auth/login")
      ) {
        forbiddenAuthNavigations.push(url);
      }
    });

    const childURL = new URL("/stacks/new", baseURL);
    childURL.searchParams.set("embedded", "true");
    await page.goto(
      `${portalOrigin}/?child=${encodeURIComponent(childURL.toString())}`,
      { waitUntil: "domcontentloaded" },
    );

    const app = page.frameLocator("#techstack-frame");
    await expect(app.getByTestId("easy-wizard")).toBeVisible();

    const appFrame = await page
      .locator("#techstack-frame")
      .elementHandle()
      .then((handle) => handle?.contentFrame());
    if (!appFrame) throw new Error("TechStack iframe did not attach");

    await expect
      .poll(
        async () =>
          page.evaluate(() =>
            (
              window as unknown as {
                portalMessages: Array<{ data?: { type?: string } }>;
              }
            ).portalMessages.some((msg) => msg.data?.type === "auth-request"),
          ),
        { message: "portal should receive an embedded auth-token request" },
      )
      .toBe(true);

    await app.getByTestId("easy-feature-storage").click();
    await app.getByTestId("wizard-next").click();
    await expect(app.getByTestId("easy-step-2")).toBeVisible();
    await app.getByTestId("server-branch-new").click();
    await app.getByTestId("server-mode-kombify-cloud").click();
    await expect(app.getByTestId("managed-provider-selector")).toBeVisible();
    await app.getByText("Provider & server details", { exact: true }).click();
    await app.getByTestId("managed-provider-centron").click();
    await expect(
      app
        .getByTestId("managed-provider-centron")
        .locator('input[type="radio"]'),
    ).toBeChecked();
    await app.getByTestId("wizard-next").click();
    await expect(app.getByTestId("easy-step-3")).toBeVisible();
    await app.getByTestId("easy-access-anywhere").click();
    await app.getByTestId("wizard-next").click();
    await expect(app.getByTestId("easy-step-4")).toBeVisible();
    await app.getByTestId("easy-users-solo").click();
    await app.getByTestId("wizard-next").click();
    await expect(app.getByTestId("easy-step-5")).toBeVisible();
    await expect(app.getByTestId("wizard-create")).toBeVisible();

    const createResponse = page.waitForResponse((response) => {
      const pathname = new URL(response.url()).pathname;
      return (
        response.request().method() === "POST" &&
        (pathname === "/api/v1/wizard/runs" ||
          pathname === "/v1/techstack/wizard/runs")
      );
    });
    await app.getByTestId("wizard-create").click();
    await expect(createResponse).resolves.toBeTruthy();

    await appFrame.waitForURL(/\/stacks\/creating\?.*stack_id=/, {
      timeout: 15_000,
    });
    await expect(app.getByText("Session Expired")).toHaveCount(0);
    await expect(app.getByText("Continue with Auth0")).toHaveCount(0);

    const portalVerify = apiCalls.find(
      (call) =>
        call.method === "POST" && call.path === "/api/v1/auth/portal-verify",
    );
    expect(portalVerify?.body).toMatchObject({ token: "portal-token-e2e" });

    const createRun = apiCalls.find(
      (call) => call.method === "POST" && call.path === "/api/v1/wizard/runs",
    );
    expect(createRun?.headers.authorization).toBeUndefined();
    expect(createRun?.headers.cookie).toContain(
      "techstack_session=embedded-session-e2e",
    );
    expect(createRun?.headers["x-csrf-token"]).toBe("csrf-token-e2e");
    expect(createRun?.headers["x-idempotency-key"]).toBeTruthy();
    expect(createRun?.body).toMatchObject({
      intent: {
        schema: "techstack.wizard-intent/v1",
        run_kind: "first-run",
        server: { transport: "kombify-cloud" },
        kit_assignment: { mode: "found" },
      },
      services: expect.any(Array),
      managed: expect.objectContaining({
        provider_id: "centron",
      }),
    });

    const embeddedNav = app.locator("nav");
    for (const destination of [
      { label: /services/i, path: "/services" },
      { label: /monitoring/i, path: "/monitoring" },
      { label: /wallet/i, path: "/wallet" },
      { label: /dashboard/i, path: "/dashboard" },
    ]) {
      await embeddedNav.getByRole("link", { name: destination.label }).click();
      await appFrame.waitForURL(
        (url) =>
          url.origin === new URL(baseURL).origin &&
          url.pathname === destination.path,
        { timeout: 15_000 },
      );
      await expect(app.getByText("Dieser Inhalt ist blockiert")).toHaveCount(0);
      await expect(app.getByText("Authentication Failed")).toHaveCount(0);
    }

    const portalExchanges = apiCalls.filter(
      (call) =>
        call.method === "POST" && call.path === "/api/v1/auth/portal-verify",
    );
    expect(
      portalExchanges,
      "reactive iframe navigation must reuse the established portal session",
    ).toHaveLength(1);

    expect(forbiddenAuthNavigations).toEqual([]);
  });
});

async function installEmbeddedSaaSApi(
  page: Page,
  apiCalls: ApiCall[],
  forbiddenAuthNavigations: string[],
) {
  await page.route("**/*", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path =
      url.origin === "https://api.kombify.io" &&
      url.pathname.startsWith("/v1/techstack/")
        ? `/api/v1/${url.pathname.slice("/v1/techstack/".length)}`
        : url.pathname;
    const method = request.method();

    if (isAuth0Host(url.hostname) || path === "/api/v2/auth/login") {
      forbiddenAuthNavigations.push(request.url());
      await route.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({
          error: "Auth0 iframe navigation is forbidden in embedded mode",
        }),
      });
      return;
    }

    if (
      url.origin === "https://api.kombify.io" &&
      (url.pathname === "/v1/ai/models" ||
        url.pathname === "/v1/agents/harnesses")
    ) {
      await json(route, { data: [] });
      return;
    }

    if (!path.startsWith("/api/")) {
      await route.fallback();
      return;
    }

    const body = parseBody(request.postData());
    apiCalls.push({
      method,
      path,
      headers: lowerCaseHeaders(request.headers()),
      body,
    });

    switch (`${method} ${path}`) {
      case "GET /api/v1/client/bootstrap":
        await json(route, {
          data: {
            edition: "saas",
            deployment_mode: "saas",
            kombify_edition: "cloud",
            version: "e2e",
            public_origin: "",
            telemetry: {
              sentry: { dsn: "", environment: "", release: "" },
              posthog: { key: "", host: "", environment: "" },
            },
          },
        });
        return;
      case "GET /api/v1/auth/mode":
        await json(route, {
          data: {
            mode: "cloud",
            deployment_mode: "saas",
            is_first_run: false,
            cloud_auth_url: "/api/v2/auth/login",
            portal_url: portalOrigin,
            allow_local_login: false,
          },
        });
        return;
      case "GET /api/v1/info":
        await json(route, { data: { version: "e2e", service: "techstack" } });
        return;
      case "GET /api/v1/instance":
        await json(route, {
          data: {
            id: "instance-e2e",
            version: "e2e",
            created_at: "2026-05-19T00:00:00Z",
          },
        });
        return;
      case "GET /api/v1/features":
        await json(route, {
          data: {
            security: [],
            beta: [
              "native_v2_wizard",
              "monthly_runtime",
              "monthly_runtime_cloudkit",
              "monthly_runtime_centron",
            ].map((key) => ({
              key,
              name: key,
              enabled: true,
              locked: false,
              requires_consent: false,
              has_consent: false,
              risk_level: "high",
              description: "",
              category: "beta",
            })),
            ux: [],
          },
        });
        return;
      case "GET /api/v1/auth/stack-identity":
        await json(route, { data: { editable: true } });
        return;
      case "GET /api/v1/discovery/networks":
        await json(route, { data: { networks: [] } });
        return;
      case "GET /api/v2/auth/providers":
        await json(route, {
          providers: [
            {
              id: "auth0",
              kind: "auth0",
              issuer: "https://login.kombify.io/",
            },
          ],
        });
        return;
      case "GET /api/v2/whoami":
        if (
          !request
            .headers()
            .cookie?.includes("techstack_session=embedded-session-e2e")
        ) {
          await json(route, { error: "missing session token" }, 401);
          return;
        }
        await json(route, {
          subject: "auth0|user-e2e",
          tenantId: "tenant-e2e",
          orgId: "org-e2e",
          email: "portal-user@example.test",
          provider: "cloud",
          role: "owner",
        });
        return;
      case "GET /api/v1/csrf":
        await json(route, { token: "csrf-token-e2e" }, 200, {
          "X-CSRF-Token": "csrf-token-e2e",
        });
        return;
      case "POST /api/v1/auth/portal-verify":
        await json(
          route,
          {
            data: {
              cloud_user: {
                sub: "auth0|user-e2e",
                email: "portal-user@example.test",
                name: "Portal User",
                is_admin: true,
              },
            },
          },
          200,
          {
            "Set-Cookie":
              "techstack_session=embedded-session-e2e; Path=/; HttpOnly; SameSite=Lax",
          },
        );
        return;
      case "POST /api/v1/wizard/runs":
        await json(
          route,
          {
            data: {
              run_id: "run-e2e",
              run_kind: "first-run",
              homelab_id: "homelab-e2e",
              kit_assignment_mode: "found",
              kit_slug: "cloud-kit",
              stack_id: "stack-e2e",
              server_id: "server-e2e",
              node_id: "node-e2e",
              job_id: "job-e2e",
              name: "homelab",
              state: "provisioning",
            },
          },
          202,
        );
        return;
      case "GET /api/v1/jobs/job-e2e":
        await json(route, {
          data: {
            id: "job-e2e",
            type: "provision",
            state: "completed",
            progress: 100,
            stack_id: "stack-e2e",
            current_step: "completed",
            message: "Configuration prepared",
            created_at: "2026-05-19T00:00:00Z",
            updated_at: "2026-05-19T00:00:01Z",
          },
        });
        return;
      default:
        await json(
          route,
          { error: `Unhandled embedded SaaS E2E API route: ${method} ${path}` },
          404,
        );
    }
  });
}

function parseBody(raw: string | null): unknown {
  if (!raw) return null;
  try {
    return JSON.parse(raw);
  } catch {
    return raw;
  }
}

function lowerCaseHeaders(
  headers: Record<string, string>,
): Record<string, string> {
  return Object.fromEntries(
    Object.entries(headers).map(([key, value]) => [key.toLowerCase(), value]),
  );
}

async function json(
  route: Route,
  body: unknown,
  status = 200,
  headers: Record<string, string> = {},
) {
  await route.fulfill({
    status,
    contentType: "application/json",
    headers,
    body: JSON.stringify(body),
  });
}

function escapeHtml(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/"/g, "&quot;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}
