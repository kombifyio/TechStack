import { expect, test, type Page } from "@playwright/test";

import {
  authenticateRuntimeUser,
  captureGatewayApiSession,
} from "./runtime-auth";

const PRODUCT_BASE = requireLiveProductURL(
  process.env.TECHSTACK_RUNTIME_E2E_PRODUCT_URL ??
    process.env.TECHSTACK_E2E_PRODUCT_URL ??
    process.env.PLAYWRIGHT_BASE_URL ??
    "https://techstack.kombify.io",
);
const API_BASE = requireLiveProductURL(
  process.env.TECHSTACK_RUNTIME_E2E_API_URL ??
    process.env.TECHSTACK_API_URL ??
    PRODUCT_BASE,
);
const SESSION_COOKIE_NAME =
  process.env.TECHSTACK_E2E_SESSION_COOKIE_NAME ?? "techstack_session";
const tierClaim = "https://kombify.io/tier";
const entitlementsClaim = "https://kombify.io/entitlements";
const commonEntitlements = [
  "cloud.runtime.credits.ayn",
  "techstack.managed.runtime",
  "techstack.managed.runtime.cloudkit",
] as const;
const managedProviders = ["centron", "ionos"] as const;
type ManagedProviderID = (typeof managedProviders)[number];

function selectedManagedProviders(): ManagedProviderID[] {
  const requested = (process.env.TECHSTACK_RUNTIME_E2E_PROVIDER_ID ?? "all")
    .trim()
    .toLowerCase();
  if (!requested || requested === "all") return [...managedProviders];
  if (managedProviders.includes(requested as ManagedProviderID)) {
    return [requested as ManagedProviderID];
  }
  throw new Error(
    "TECHSTACK_RUNTIME_E2E_PROVIDER_ID must be exactly centron, ionos, or all",
  );
}

function decodeGatewayTokenClaims(token: string): Record<string, unknown> {
  const parts = token.split(".");
  if (parts.length !== 3) {
    throw new Error("Gateway bearer token is not a JWT with a payload");
  }
  try {
    const decoded = JSON.parse(
      Buffer.from(parts[1], "base64url").toString("utf8"),
    ) as unknown;
    if (!decoded || typeof decoded !== "object" || Array.isArray(decoded)) {
      throw new Error("invalid JWT claim object");
    }
    return decoded as Record<string, unknown>;
  } catch {
    // Never include token material or decoded claims in the failure output.
    throw new Error("Gateway bearer token payload cannot be decoded safely");
  }
}

async function clearBrowserAuthState(page: Page) {
  // Playwright creates a new context per test, but clear both cookies and the
  // product storage explicitly so this check cannot reuse an earlier browser
  // session or cached access token.
  await page.context().clearCookies();
  await page.goto(`/login?runtime_authority_fresh=${Date.now()}`, {
    waitUntil: "domcontentloaded",
  });
  await page.evaluate(() => {
    window.localStorage.clear();
    window.sessionStorage.clear();
  });
}

function assertFreshCloudClaims(
  claims: Record<string, unknown>,
  providerID: ManagedProviderID,
  issuedAfterSeconds: number,
) {
  expect(claims[tierClaim]).toBe("ayn");
  const entitlements = claims[entitlementsClaim];
  expect(Array.isArray(entitlements)).toBe(true);
  const granted = new Set(
    Array.isArray(entitlements)
      ? entitlements.filter(
          (value): value is string => typeof value === "string",
        )
      : [],
  );
  for (const entitlement of [
    ...commonEntitlements,
    `techstack.managed.runtime.${providerID}`,
  ]) {
    expect(granted.has(entitlement)).toBe(true);
  }
  const issuedAt = claims.iat;
  expect(typeof issuedAt).toBe("number");
  expect(issuedAt as number).toBeGreaterThanOrEqual(issuedAfterSeconds);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function managedAuthorityFailureReason(value: unknown): string {
  if (!isRecord(value) || !isRecord(value.error)) return "";
  const details = isRecord(value.error.details) ? value.error.details : {};
  return typeof details.reason_code === "string"
    ? details.reason_code.trim()
    : "";
}

for (const providerID of selectedManagedProviders()) {
  test(`fresh cloud token proves request-bound managed authority for ${providerID}`, async ({
    page,
  }) => {
    await clearBrowserAuthState(page);
    // Permit normal clock skew while still rejecting a browser-cached token.
    const issuedAfterSeconds = Math.floor(Date.now() / 1000) - 120;
    await authenticateRuntimeUser("cloud", page, {
      productBase: PRODUCT_BASE,
      apiBase: API_BASE,
      sessionCookieName: SESSION_COOKIE_NAME,
    });
    const gateway = await captureGatewayApiSession(page);
    const claims = decodeGatewayTokenClaims(gateway.token);
    assertFreshCloudClaims(claims, providerID, issuedAfterSeconds);

    await test.info().attach(`managed-runtime-claims-${providerID}.json`, {
      body: Buffer.from(
        `${JSON.stringify({
          kind: "techstack-managed-runtime-token-claims/v1",
          provider_id: providerID,
          fresh_auth0_token: {
            token_material_omitted: true,
            subject_omitted: true,
            issued_after_test_start: true,
          },
          claims: {
            tier: "ayn",
            required_entitlements: [
              ...commonEntitlements,
              `techstack.managed.runtime.${providerID}`,
            ],
          },
        })}\n`,
        "utf8",
      ),
      contentType: "application/json",
    });

    const response = await fetch(
      `${gateway.apiBase}/managed-runtimes/authority?provider_id=${encodeURIComponent(providerID)}`,
      { headers: { Authorization: `Bearer ${gateway.token}` } },
    );
    if (!response.ok) {
      // Surface only the stable, server-owned reason code. Tenant diagnostics
      // and response bodies remain omitted, but an opaque 503 cannot guide an
      // operator toward entitlement, activation, or custody remediation.
      const failureEnvelope = (await response
        .json()
        .catch(() => null)) as unknown;
      const reason = managedAuthorityFailureReason(failureEnvelope);
      throw new Error(
        `Gateway managed-runtime authority preflight returned HTTP ${response.status}${reason ? ` (${reason})` : ""}`,
      );
    }
    const envelope = (await response.json()) as unknown;
    const data =
      isRecord(envelope) && isRecord(envelope.data) ? envelope.data : envelope;
    if (!isRecord(data)) {
      throw new Error(
        "Gateway managed-runtime authority preflight returned no data object",
      );
    }
    expect(data.provider_id).toBe(providerID);
    expect(data.budget_key).toBe("cloud.runtime.credits");
    expect(data.decision_source).toBe(
      "edge_v2_entitlement+signed_budget:cloud.runtime.credits#managed_servers",
    );
    expect(data.request_bound).toBe(true);
    expect(data.admission_preflight).toBe(true);

    await test.info().attach(`managed-runtime-authority-${providerID}.json`, {
      body: Buffer.from(
        `${JSON.stringify({
          kind: "techstack-managed-runtime-authority-preflight/v1",
          provider_id: providerID,
          gateway: {
            budget_key: "cloud.runtime.credits",
            request_bound: true,
            admission_preflight: true,
          },
        })}\n`,
        "utf8",
      ),
      contentType: "application/json",
    });
  });
}

function requireLiveProductURL(value: string): string {
  const parsed = new URL(value);
  const host = parsed.hostname.toLowerCase();
  if (
    parsed.protocol !== "https:" ||
    host === "localhost" ||
    host === "127.0.0.1" ||
    host === "::1"
  ) {
    throw new Error(
      "Managed runtime authority preflight requires a live HTTPS SaaS URL",
    );
  }
  parsed.hash = "";
  parsed.search = "";
  return parsed.toString().replace(/\/+$/g, "");
}
