import { readFileSync } from "node:fs";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { type Page, type Route } from "@playwright/test";
import { test, expect } from "./fixtures";
import { requireAppBase } from "../helpers/test-utils";
import {
  accessTokenFor,
  WALLET_API_ORIGIN,
  WalletMock,
} from "../helpers/wallet-mock";

// Hosted Techstack keeps credentials in the person's central kombify Wallet:
// the browser seals a value to the Wallet Store before it leaves, and the
// Gateway's Wallet API is the only place it goes. These journeys run the app
// the way kombify Cloud embeds it, with the portal answering the frame's
// gateway-token request, against a Wallet stand-in that opens and seals
// envelopes with an independent implementation of the transport.

const OWNER = "auth0|wallet-ui-owner";
const sessionCookie = "techstack_session=wallet-session";
const releasedCatalog = JSON.parse(
  readFileSync(
    new URL("../../static/preview-catalogs/latest.json", import.meta.url),
    "utf8",
  ),
);

let portalServer: Server;
let portalOrigin: string;

test.beforeAll(async () => {
  const token = accessTokenFor(OWNER);
  portalServer = createServer((req, res) => {
    const child = new URL(req.url ?? "/", "http://127.0.0.1").searchParams.get(
      "child",
    );
    res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
    res.end(`<!doctype html><html><body>
<iframe id="techstack-frame" src="${(child ?? "").replace(/&/g, "&amp;").replace(/"/g, "&quot;")}"
  sandbox="allow-same-origin allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-storage-access-by-user-activation"
  style="width:1200px;height:800px;border:0"></iframe>
<script>
  window.addEventListener("message", (event) => {
    const type = event.data && event.data.type;
    if (type === "auth-request") {
      event.source.postMessage({ type: "auth-token", token: "portal-token" }, event.origin);
    }
    if (type === "gateway-token-request") {
      event.source.postMessage({
        type: "gateway-token",
        token: ${JSON.stringify(token)},
        audience: "https://api.kombify.io",
        expiresAt: Date.now() + 600000
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
  id: "stack-wallet",
  name: "basement",
  mode: "easy",
  status: "running",
  state: "running",
  provider: "local",
  services: [],
  created_at: "2026-10-07T09:00:00Z",
  updated_at: "2026-10-07T09:00:00Z",
};

// A Cloud-hosted Techstack answering as the backend does, with the central
// Wallet as its Wallet source. Routes in `extra` are keyed "METHOD /path".
function hostedBackend(
  extra: Record<string, (route: Route) => Promise<void>> = {},
) {
  return async (route: Route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path =
      url.origin === WALLET_API_ORIGIN &&
      url.pathname.startsWith("/v1/techstack/")
        ? `/api/v1/${url.pathname.slice("/v1/techstack/".length)}`
        : url.pathname;
    if (!path.startsWith("/api/") || request.method() === "OPTIONS") {
      await route.fallback();
      return;
    }
    const key = `${request.method()} ${path}`;
    if (extra[key]) return extra[key](route);
    switch (key) {
      case "GET /api/v1/stackkits/use-cases":
        return fulfillJson(route, apiEnvelope(releasedCatalog));
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
            wallet_source: "kombify",
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
          subject: OWNER,
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
              sub: OWNER,
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
      case "GET /api/v1/servers":
        return fulfillJson(route, apiEnvelope([]));
      default:
        if (path.startsWith("/api/v1/jobs")) {
          return fulfillJson(route, apiEnvelope({ items: [] }));
        }
        return fulfillJson(route, { error: `unmocked ${key}` }, 404);
    }
  };
}

// Opens a page inside the Cloud frame the way the portal does: the frame loads
// the dashboard, whose portal sign-in establishes the session, and the portal
// then navigates it. A direct deep link would reach the page before that
// session exists.
async function openInFrame(page: Page, origin: string, path: string) {
  const child = `${origin}/dashboard?embedded=true&host_navigation=true`;
  await page.goto(`${portalOrigin}/?child=${encodeURIComponent(child)}`);
  const app = page.frameLocator("#techstack-frame");
  await expect(app.getByRole("main")).toBeVisible();
  await page.evaluate((target) => {
    const frame = document.querySelector<HTMLIFrameElement>("#techstack-frame");
    frame?.contentWindow?.postMessage({ type: "navigate", path: target }, "*");
  }, path);
  const frame = page
    .frames()
    .find((candidate) => candidate.url().startsWith(`${origin}/`));
  if (!frame) throw new Error("The Techstack frame did not load");
  const target = new URL(path, origin);
  await frame.waitForURL((url) => url.pathname === target.pathname);
  return app;
}

test("the hosted Wallet seals a login in the browser, reveals it back and lets Techstack use it", async ({
  context,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  const wallet = new WalletMock(OWNER);
  await context.route("**/*", hostedBackend());
  await context.route(`${WALLET_API_ORIGIN}/v1/wallet/**`, wallet.handle);

  const page = await context.newPage();
  const app = await openInFrame(page, origin, "/wallet");
  await expect(app.getByTestId("kombify-wallet-page")).toBeVisible();
  await expect(app.getByText("Your Wallet is empty")).toBeVisible();

  // Create a login: the value is composed and sealed in the browser.
  await app.getByTestId("wallet-add").click();
  await app.getByRole("radio", { name: "Login" }).check();
  await app.getByLabel("Name", { exact: true }).fill("Router login");
  await app.getByLabel("Username").fill("admin");
  await app.getByLabel("Password", { exact: true }).fill("hunter2-secret");
  await app.getByTestId("wallet-save").click();
  const row = app
    .getByTestId("wallet-item")
    .filter({ hasText: "Router login" });
  await expect(row).toBeVisible();

  // The Wallet Store opened exactly the sealed value; no request carried it in
  // the clear.
  const stored = [...wallet.items.values()];
  expect(stored).toHaveLength(1);
  expect(JSON.parse(stored[0].value)).toEqual({
    username: "admin",
    password: "hunter2-secret",
  });
  expect(wallet.requests.some((r) => r.body.includes("hunter2-secret"))).toBe(
    false,
  );

  // The card opens the item's detail sheet, deep-linked by item id; reveal
  // there decrypts with the browser's ephemeral key.
  await row.getByRole("button", { name: "Router login · Login" }).click();
  const detail = app.getByTestId("wallet-item-detail");
  await expect(detail).toBeVisible();
  await app.getByRole("button", { name: "Reveal", exact: true }).click();
  await app.getByRole("button", { name: "Show", exact: true }).click();
  await expect(app.getByTestId("wallet-revealed-value")).toHaveText([
    "admin",
    "hunter2-secret",
  ]);
  await app.getByRole("button", { name: "Close", exact: true }).first().click();

  // The owner decides what Techstack may do with the item; the sheet is back
  // once the reveal dialog is closed.
  const use = detail.getByTestId("wallet-techstack-use");
  await expect(use).toBeEnabled();
  await use.check();
  await expect.poll(() => wallet.techstackMayUse(stored[0].itemId)).toBe(true);

  await page.keyboard.press("Escape");
  await expect(detail).toBeHidden();

  const creators = app.getByTestId("wallet-creator-toggle");
  await expect(creators).toBeEnabled();
  await creators.check();
  await expect.poll(() => wallet.creators.has("techstack")).toBe(true);
});

// The wizard reaches a server with a Wallet item, never with a password or key
// label of its own. Items are offered for the host and sign-in method in use,
// and picking one is what lets Techstack use it.
async function openRemoteServerStep(app: ReturnType<Page["frameLocator"]>) {
  await app
    .getByTestId("easy-feature-storage")
    .getByRole("button", { name: /^Add to your kit / })
    .click();
  await app.getByTestId("wizard-next").click();
  await app.getByTestId("server-branch-owned").click();
  await app.getByTestId("server-mode-connect-remote").click();
  await app.getByTestId("remote-server-host").fill("server.test.local");
}

const keyValue = JSON.stringify({
  host: "server.test.local",
  user: "root",
  privateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nseeded\n",
});

test("the wizard picks or creates server access in the Wallet and sends only its id", async ({
  context,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  const wallet = new WalletMock(OWNER);
  const fitting = wallet.seed({
    itemType: "node_access_profile",
    displayName: "Edge router access",
    description: "ssh root@server.test.local (key)",
    value: keyValue,
  });
  wallet.seed({
    itemType: "node_access_profile",
    displayName: "Other host access",
    description: "ssh root@other.test.local (key)",
    value: keyValue,
  });
  wallet.seed({
    itemType: "node_access_profile",
    displayName: "Password access",
    description: "ssh root@server.test.local (password)",
    value: JSON.stringify({
      host: "server.test.local",
      user: "root",
      password: "seeded",
    }),
  });

  const connectionTests: Record<string, unknown>[] = [];
  await context.route(
    "**/*",
    hostedBackend({
      "POST /api/v1/wizard/remote/test-ssh": async (route) => {
        connectionTests.push(route.request().postDataJSON());
        await fulfillJson(
          route,
          apiEnvelope({ success: true, message: "Connected" }),
        );
      },
    }),
  );
  await context.route(`${WALLET_API_ORIGIN}/v1/wallet/**`, wallet.handle);

  const page = await context.newPage();
  const app = await openInFrame(page, origin, "/stacks/new");
  await openRemoteServerStep(app);

  // No plaintext credential fields exist in the hosted wizard.
  await expect(app.getByTestId("remote-server-password")).toHaveCount(0);
  await expect(app.getByTestId("remote-server-key-label")).toHaveCount(0);

  // Without a Wallet item the step cannot go on and nothing is tested.
  await expect(app.getByTestId("wizard-next")).toBeDisabled();
  await app.getByTestId("remote-server-test-connection").click();
  await expect(app.getByTestId("remote-server-test-result")).toContainText(
    "Choose or create the server access",
  );
  expect(connectionTests).toHaveLength(0);

  // Only access for this host and the chosen method is offered.
  const select = app.getByTestId("wallet-access-select");
  await expect(select).toBeEnabled();
  await expect(select.getByRole("option")).toHaveText([
    "Choose server access",
    "Edge router access",
  ]);

  // Picking it lets Techstack use it, and the test names it with the method.
  await select.selectOption({ label: "Edge router access" });
  await expect(app.getByTestId("wallet-techstack-notice")).toBeVisible();
  expect(wallet.techstackMayUse(fitting)).toBe(true);
  await expect(app.getByTestId("wizard-next")).toBeEnabled();
  await app.getByTestId("remote-server-test-connection").click();
  await expect.poll(() => connectionTests.length).toBe(1);
  expect(connectionTests[0]).toEqual({
    host: "server.test.local",
    port: 22,
    user: "root",
    auth_method: "ssh-key",
    ssh_wallet_item_id: fitting,
  });

  // Another sign-in method lets go of an item that cannot serve it.
  await app
    .getByTestId("remote-server-config")
    .getByText("Password", { exact: true })
    .click();
  await expect(app.getByTestId("wizard-next")).toBeDisabled();
  await expect(select.getByRole("option")).toHaveText([
    "Choose server access",
    "Password access",
  ]);

  // Creating new access seals host, user and password in the browser, writes
  // the wizard's host into the item, lets Techstack use it and selects it.
  await app.getByTestId("wallet-picker-create").click();
  const dialog = app.getByRole("dialog");
  await expect(dialog.getByLabel("Host")).toHaveValue("server.test.local");
  await expect(dialog.getByLabel("Host")).toBeDisabled();
  await dialog.getByRole("textbox", { name: "Password" }).fill("ssh-pass-1");
  await dialog.getByTestId("wallet-save").click();
  const createdItem = () =>
    [...wallet.items.values()].find(
      (item) => item.displayName === "server.test.local",
    );
  await expect.poll(createdItem).toBeDefined();
  const created = createdItem()!;
  expect(JSON.parse(created.value)).toEqual({
    host: "server.test.local",
    user: "root",
    password: "ssh-pass-1",
  });
  expect(created.description).toBe("ssh root@server.test.local (password)");
  expect(wallet.requests.some((r) => r.body.includes("ssh-pass-1"))).toBe(
    false,
  );
  await expect.poll(() => wallet.techstackMayUse(created.itemId)).toBe(true);
  await expect(select).toHaveValue(created.itemId);
  await app.getByTestId("remote-server-test-connection").click();
  await expect.poll(() => connectionTests.length).toBe(2);
  expect(connectionTests[1]).toMatchObject({
    auth_method: "password",
    ssh_wallet_item_id: created.itemId,
  });
});

test("the wizard preselects the one fitting item Techstack may already use, and asks before it uses any other", async ({
  context,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  const wallet = new WalletMock(OWNER);
  const allowed = wallet.seed({
    itemType: "node_access_profile",
    displayName: "Edge router access",
    description: "ssh root@server.test.local (key)",
    value: keyValue,
  });
  wallet.allow(allowed, "techstack", "use");
  await context.route("**/*", hostedBackend());
  await context.route(`${WALLET_API_ORIGIN}/v1/wallet/**`, wallet.handle);

  const page = await context.newPage();
  const app = await openInFrame(page, origin, "/stacks/new");
  await openRemoteServerStep(app);
  await expect(app.getByTestId("wallet-access-select")).toHaveValue(allowed);
  await expect(app.getByTestId("wizard-next")).toBeEnabled();
  // Choosing it for the owner changed nothing in the Wallet.
  expect(
    wallet.requests.filter(
      (request) =>
        request.method === "POST" && request.path.endsWith("/services"),
    ),
  ).toHaveLength(0);
});

test("a rollout refused for the missing Wallet right is resolved with one grant, then goes through", async ({
  context,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  const wallet = new WalletMock(OWNER);
  wallet.seed({
    itemType: "node_access_profile",
    displayName: "Edge router access",
    description: "ssh root@server.test.local (key)",
    value: keyValue,
  });
  await context.route(
    "**/*",
    hostedBackend({
      "POST /api/v1/wizard/remote/test-ssh": async (route) => {
        if (wallet.creators.has("techstack"))
          return fulfillJson(
            route,
            apiEnvelope({ success: true, message: "Connected" }),
          );
        await fulfillJson(
          route,
          {
            error_code: "TECHSTACK_WALLET_DENIED",
            reason_code: "wallet_create_right_missing",
            required_right: "create",
            retryable: false,
            user_guidance:
              "Allow Techstack to save credentials in your kombify Wallet",
          },
          409,
        );
      },
    }),
  );
  await context.route(`${WALLET_API_ORIGIN}/v1/wallet/**`, wallet.handle);

  const page = await context.newPage();
  const app = await openInFrame(page, origin, "/stacks/new");
  await openRemoteServerStep(app);
  await app
    .getByTestId("wallet-access-select")
    .selectOption({ label: "Edge router access" });
  await expect(app.getByTestId("wallet-techstack-notice")).toBeVisible();

  await app.getByTestId("remote-server-test-connection").click();
  const prompt = app.getByTestId("wallet-prompt");
  await expect(prompt).toBeVisible();
  await app.getByTestId("wallet-prompt-allow").click();
  await expect.poll(() => wallet.creators.has("techstack")).toBe(true);
  await app.getByTestId("wallet-prompt-close").click();
  await expect(prompt).toBeHidden();

  await app.getByTestId("remote-server-test-connection").click();
  await expect(app.getByTestId("remote-server-test-result")).toContainText(
    "Connected",
  );
});
