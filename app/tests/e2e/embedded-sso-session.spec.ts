import { createHmac } from "node:crypto";

import { expect, test } from "@playwright/test";

// Mock-free regression guard for the embedded-SSO 401 loop.
//
// The mock-backed embedded-saas-create.spec.ts stubs /api/v1/auth/portal-verify,
// so it can only prove the iframe *calls* portal-verify — never that the real
// backend establishes a usable session. That blind spot is exactly how the
// 401-loop shipped: portal-verify returned a body token but issued no
// techstack_session, so every subsequent API call 401'd.
//
// This spec runs against the live SaaS product / Render Preview (no mocks): the
// parent-minted SSO token is exchanged at the REAL portal-verify, and the
// resulting techstack_session cookie must authenticate a protected endpoint.
// Skipped where the shared SSO secret is unavailable (local dev); supplied via
// the configured CI secret source in CI and the Render-Preview staging gate.

function firstDefined(
  ...values: Array<string | undefined>
): string | undefined {
  for (const value of values) {
    const trimmed = value?.trim();
    if (trimmed) return trimmed;
  }
  return undefined;
}

function requireLiveHttpsOrigin(label: string, value: string): string {
  const parsed = new URL(value);
  if (parsed.protocol !== "https:") {
    throw new Error(`${label} must be a live HTTPS origin, got ${value}`);
  }
  parsed.hash = "";
  parsed.search = "";
  return parsed.toString().replace(/\/$/, "");
}

const API_BASE = requireLiveHttpsOrigin(
  "TechStack API",
  firstDefined(
    process.env.TECHSTACK_RUNTIME_E2E_API_URL,
    process.env.TECHSTACK_API_URL,
    process.env.TECHSTACK_RUNTIME_E2E_PRODUCT_URL,
    process.env.TECHSTACK_E2E_PRODUCT_URL,
    process.env.PLAYWRIGHT_BASE_URL,
  ) ?? "https://techstack.kombify.io",
);

const SESSION_COOKIE_NAME =
  process.env.TECHSTACK_E2E_SESSION_COOKIE_NAME ?? "techstack_session";

// The shared HS256 secret kombify Cloud signs SSO tokens with and TechStack
// verifies against. A mismatch here is the "secret drift" failure mode — this
// test fails loudly (portal-verify 401) instead of leaving it to manual review.
const SSO_SECRET = firstDefined(
  process.env.TECHSTACK_E2E_SSO_JWT_SECRET,
  process.env.SSO_JWT_SECRET,
  process.env.KOMBIFY_SSO_SECRET,
);

const SSO_TOOL_ID = "kombifystack";
const SSO_SUBJECT =
  process.env.TECHSTACK_E2E_SSO_SUBJECT ?? "auth0|techstack-e2e-embedded-sso";
const SSO_EMAIL =
  process.env.TECHSTACK_E2E_SSO_EMAIL ?? "embedded-sso-e2e@kombify.test";

function base64url(input: Buffer | string): string {
  return Buffer.from(input)
    .toString("base64")
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

function mintSsoToken(secret: string): string {
  const now = Math.floor(Date.now() / 1000);
  const header = base64url(JSON.stringify({ alg: "HS256", typ: "JWT" }));
  const payload = base64url(
    JSON.stringify({
      sub: SSO_SUBJECT,
      email: SSO_EMAIL,
      name: "Embedded SSO E2E",
      tool: SSO_TOOL_ID,
      iat: now,
      exp: now + 300,
    }),
  );
  const signingInput = `${header}.${payload}`;
  const signature = base64url(
    createHmac("sha256", secret).update(signingInput).digest(),
  );
  return `${signingInput}.${signature}`;
}

test.describe("Embedded SSO session (mock-free)", () => {
  test.skip(
    !SSO_SECRET,
    "SSO secret unavailable; set SSO_JWT_SECRET/KOMBIFY_SSO_SECRET (a configured secret source) to run",
  );

  test("portal-verify issues a techstack_session that authenticates the API", async ({
    playwright,
  }) => {
    const ctx = await playwright.request.newContext({ baseURL: API_BASE });
    let reprojectionCtx:
      | Awaited<ReturnType<typeof playwright.request.newContext>>
      | undefined;
    try {
      const token = mintSsoToken(SSO_SECRET as string);

      const verify = await ctx.post("/api/v1/auth/portal-verify", {
        headers: { "content-type": "application/json" },
        data: { token },
      });
      expect(
        verify.status(),
        "portal-verify must accept the parent-minted SSO token; 401 here means the Cloud/TechStack SSO secret drifted",
      ).toBe(200);

      // Regression assertion: the exchange must ESTABLISH the session the
      // request path requires, not merely return a body token.
      const session = (await ctx.storageState()).cookies.find(
        (cookie) => cookie.name === SESSION_COOKIE_NAME,
      );
      expect(
        session?.value,
        `portal-verify must set the ${SESSION_COOKIE_NAME} cookie; its absence is the embedded 401-loop bug`,
      ).toBeTruthy();

      // End-to-end: the issued session must authenticate a protected endpoint
      // through the real (SaaS Edge) request path.
      const whoami = await ctx.get("/api/v2/whoami");
      expect(
        whoami.status(),
        "the embedded SSO session must authenticate /api/v2/whoami; 401 means the request path does not honor the cookie",
      ).toBe(200);

      // Model browser cookie loss while the parent still reuses its cached,
      // valid portal token. The identical token must mint one new cookie
      // session; treating token reuse as a no-op recreates the 401 loop.
      reprojectionCtx = await playwright.request.newContext({
        baseURL: API_BASE,
      });
      const reprojection = await reprojectionCtx.post(
        "/api/v1/auth/portal-verify",
        {
          headers: { "content-type": "application/json" },
          data: { token },
        },
      );
      expect(reprojection.status()).toBe(200);
      const reprojectedSession = (
        await reprojectionCtx.storageState()
      ).cookies.find((cookie) => cookie.name === SESSION_COOKIE_NAME);
      expect(reprojectedSession?.value).toBeTruthy();
      expect((await reprojectionCtx.get("/api/v2/whoami")).status()).toBe(200);
    } finally {
      await reprojectionCtx?.dispose();
      await ctx.dispose();
    }
  });
});
