import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { type Route } from "@playwright/test";
import { test, expect } from "./fixtures";
import { requireAppBase } from "../helpers/test-utils";

// Inside kombify Cloud the Companion belongs to the host page and cannot see
// the Techstack frame. On a failed creation it answered about an unrelated
// server (owner report 2026-10-07) because Techstack never said which page and
// which stack the user was looking at. The frame now publishes that to the
// host, and answers again whenever the host asks.

const stackId = "stack-embedded-timeout";
const jobId = "job-apply-timeout";
const leaseId = "lease-embedded-timeout";
const reasonCode = "typed_stackkit_apply_timeout";
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
    headers,
    body: JSON.stringify(data),
  });
}

// A Cloud-hosted Techstack whose rollout job timed out while applying.
async function cloudBackend(route: Route) {
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
    case `GET /api/v1/jobs/${jobId}`:
      return fulfillJson(
        route,
        apiEnvelope({
          id: jobId,
          type: "deploy",
          state: "failed",
          progress: 60,
          step: "stackkit_rollout",
          error: "The StackKit rollout timed out while applying",
          created: new Date().toISOString(),
          updated: new Date().toISOString(),
          result: {
            stack_id: stackId,
            server_provisioning_mode: "kombify-cloud",
            runtime_phase: "lease_ready",
            lease_id: leaseId,
            runtime_diagnostics: { status: "failed", reason: reasonCode },
          },
        }),
      );
    default:
      return fulfillJson(route, { error: "unmocked" }, 404);
  }
}

type HostMessage = {
  type: string;
  tool?: string;
  schema?: string;
  seq?: number;
  route?: string;
  title?: string;
  state?: string;
  subject?: { resourceType: string; resourceId: string; label?: string };
  facts?: Record<string, string>;
  error_context?: { operation?: { jobId?: string; stackId?: string } };
};

test("an embedded failed creation tells the host which stack and job it shows, on request too", async ({
  context,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  await context.route("**/*", cloudBackend);

  const page = await context.newPage();
  const hostLog = async (type: string) =>
    (
      (await page.evaluate(
        () => (window as unknown as { hostLog: HostMessage[] }).hostLog,
      )) as HostMessage[]
    ).filter((message) => message.type === type);

  const child =
    `${origin}/stacks/creating?name=basement&job_id=${jobId}` +
    `&stack_id=${stackId}&embedded=true&host_navigation=true`;
  await page.goto(`${portalOrigin}/?child=${encodeURIComponent(child)}`);
  const app = page.frameLocator("#techstack-frame");
  await expect(app.getByTestId("error-details-text")).toContainText(
    reasonCode,
    { timeout: 20_000 },
  );

  // The host learns the page and the failed run, by ids and codes only.
  await expect
    .poll(async () =>
      (await hostLog("view-context")).some(
        (message) => message.state === "creation_failed",
      ),
    )
    .toBe(true);
  const failed = (await hostLog("view-context")).findLast(
    (message) => message.state === "creation_failed",
  )!;
  expect(failed.tool).toBe("kombifystack");
  expect(failed.route).toBe("/stacks/creating");
  expect(failed.subject?.resourceId).toBe(stackId);
  expect(failed.facts).toMatchObject({
    stackId,
    jobId,
    leaseId,
    reasonCode,
    outcome: "failed",
  });
  expect(JSON.stringify(failed)).not.toContain("timed out while applying");

  // The host asks again (a frame load, a refresh) and gets the same view,
  // ordered after what it already has.
  const before = (await hostLog("view-context")).length;
  await page.evaluate(() =>
    (
      window as unknown as { askForViewContext: () => void }
    ).askForViewContext(),
  );
  await expect
    .poll(async () => (await hostLog("view-context")).length)
    .toBeGreaterThan(before);
  const answer = (await hostLog("view-context")).at(-1)!;
  expect(answer.seq).toBeGreaterThan(failed.seq!);
  expect(answer.facts).toEqual(failed.facts);

  // The same failure can be handed to the host's Companion from the page.
  await app.getByTestId("ask-kombify-ai-error").click();
  await expect
    .poll(async () => (await hostLog("ai-handover")).length)
    .toBeGreaterThan(0);
  expect(
    (await hostLog("ai-handover"))[0]?.error_context?.operation,
  ).toMatchObject({ jobId, stackId });
});
