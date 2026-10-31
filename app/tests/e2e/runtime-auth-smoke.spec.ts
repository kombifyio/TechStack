import { expect, test, type Page } from "@playwright/test";
import { createHash, randomUUID } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { isDeepStrictEqual } from "node:util";

import {
  authenticateRuntimeUser,
  captureGatewayApiSession,
  fetchBrowserWhoAmI,
  type RuntimeAuthRole,
  type RuntimeGatewaySession,
} from "./runtime-auth";
import {
  assertRuntimeInventoryExpectation,
  runtimeInventoryExpectation,
  type RuntimeInventoryAssertionResult,
  type RuntimeInventoryExpectation,
} from "./runtime-inventory-profile";

const PRODUCT_BASE = requireLiveProductURL(
  "TechStack product",
  firstProductURL(
    process.env.TECHSTACK_RUNTIME_E2E_PRODUCT_URL,
    process.env.TECHSTACK_E2E_PRODUCT_URL,
    process.env.PLAYWRIGHT_BASE_URL,
  ) || "https://techstack.kombify.io",
);
const API_BASE = requireLiveProductURL(
  "TechStack API",
  firstProductURL(
    process.env.TECHSTACK_RUNTIME_E2E_API_URL,
    process.env.TECHSTACK_API_URL,
    PRODUCT_BASE,
  ) || PRODUCT_BASE,
);
const SESSION_COOKIE_NAME =
  process.env.TECHSTACK_E2E_SESSION_COOKIE_NAME ?? "techstack_session";
const RUNTIME_AUTH_OPTIONS = {
  productBase: PRODUCT_BASE,
  apiBase: API_BASE,
  sessionCookieName: SESSION_COOKIE_NAME,
};
const INVENTORY_TOOLS = [
  "get_stack_operations",
  "list_servers",
  "list_services",
  "server_access_context",
  "server_health",
].sort();
const INVENTORY_EXPECTATION = runtimeInventoryExpectation(process.env);
const NIGHTLY =
  process.env.TECHSTACK_E2E_EXPECTED_CHANNEL?.trim() === "preview";
const ONBOARDING_CONTEXT_JOURNEY = process.env.TECHSTACK_RUNTIME_E2E_ONBOARDING_CONTEXT === "1";
const PERSONAL_SETTINGS_CONTEXT_JOURNEY = process.env.TECHSTACK_RUNTIME_E2E_PERSONAL_SETTINGS_CONTEXT === "1";
const STAGE_CONTEXT_JOURNEY = ONBOARDING_CONTEXT_JOURNEY || PERSONAL_SETTINGS_CONTEXT_JOURNEY;
// Both users propose only within their own Stage tenant through signed Gateway
// context. Provider execution retains its separate assigned-owner guard.
const STAGE_PROPOSAL_FIXTURE = "TEST_ALL_YOU_NEED_USER";
const STAGE_COHORT_FIXTURE = "TEST_RIL_PRO_USER";
if (STAGE_CONTEXT_JOURNEY && (!NIGHTLY || new URL(PRODUCT_BASE).origin !== "https://preview.techstack.kombify.io")) {
  throw new Error("The isolation journey requires the fixed managed Stage product");
}
if (ONBOARDING_CONTEXT_JOURNEY && PERSONAL_SETTINGS_CONTEXT_JOURNEY) {
  throw new Error("Select one Stage context cohort per runtime journey");
}
if (PERSONAL_SETTINGS_CONTEXT_JOURNEY) {
  const required = ["TECHSTACK_E2E_EXPECTED_GATEWAY_SHA", "TEST_RIL_PRO_USER_EMAIL", "TEST_RIL_PRO_USER_PASSWORD",
    "TEST_ALL_YOU_NEED_USER_EMAIL", "TEST_ALL_YOU_NEED_USER_PASSWORD"];
  if (required.some((name) => !process.env[name]?.trim()) ||
    !/^[a-f0-9]{40}$/.test(process.env.TECHSTACK_E2E_EXPECTED_GATEWAY_SHA ?? "")) {
    throw new Error("Personal settings isolation requires the qualified shared Gateway SHA and both canonical Stage fixtures");
  }
}

// Data protection profile journey step (DATA-ARCHITECTURE.md §5.6). Set only
// where the profile chain is switched on: "off" (Techstack verification off),
// "standard", or the axes of the test user's profile joined by "+" ("eu",
// "zero", "eu+zero"). The read goes through the Gateway, which signs the
// profile; the product origin never carries one.
const EXPECTED_DATA_PROFILE =
  process.env.TECHSTACK_E2E_EXPECTED_DATA_PROFILE?.trim().toLowerCase() ?? "";
const GATEWAY_TECHSTACK_BASE = (
  process.env.TECHSTACK_E2E_GATEWAY_API_URL?.trim() ||
  "https://api.kombify.io/v1/techstack"
).replace(/\/+$/g, "");

const allRoles: RuntimeAuthRole[] = ["oneliner", "remote", "cloud"];
const roles = runtimeAuthRolesFromEnv(
  process.env.TECHSTACK_RUNTIME_E2E_AUTH_ROLES,
);
if (PERSONAL_SETTINGS_CONTEXT_JOURNEY && !roles.includes("cloud")) {
  throw new Error("The selected personal settings journey requires the cloud runtime role");
}

type WizardPageContext = {
  surface?: string;
  route?: string;
  page?: { facts?: Record<string, string> };
};
type ContextWindow = Window & { __wizardContexts?: WizardPageContext[] };

test("Render refuses file bytes and requires authentication for artifact metadata", async ({
  request,
}) => {
  test.skip(
    process.env.TECHSTACK_E2E_EXPECT_RENDER_DOWNLOAD_DENIAL !== "1",
    "select only for a Render-hosted product origin",
  );
  // Installers and exports remain forbidden on Render. Agent routes authorize
  // Cloudflare/R2 delivery with bounded metadata, so anonymous requests must
  // stop at authentication rather than being mistaken for file downloads.
  const cases = [
    { method: "GET", route: "/install.sh", status: 503 },
    { method: "GET", route: "/install.ps1", status: 503 },
    {
      method: "GET",
      route: "/api/v1/stacks/render-download-probe/export",
      accept: "application/json",
      status: 503,
    },
    {
      method: "GET",
      route: "/api/v1/stacks/render-download-probe/export",
      accept: "application/yaml",
      status: 503,
    },
    { method: "POST", route: "/api/v1/agent/binary/linux/amd64", status: 401 },
    {
      method: "POST",
      route: "/api/v1/agent/stackkit-release/linux/amd64",
      status: 401,
    },
  ];
  for (const entry of cases) {
    for (const range of ["", "bytes=0-0"]) {
      const response = await request.fetch(`${API_BASE}${entry.route}`, {
        method: entry.method,
        headers: {
          Accept: entry.accept ?? "*/*",
          ...(range ? { Range: range, "If-None-Match": '"probe"' } : {}),
        },
      });
      expect(response.status(), `${entry.method} ${entry.route} ${range}`).toBe(
        entry.status,
      );
      expect(response.headers()["content-type"]).toContain("application/json");
      expect(response.headers()["content-disposition"]).toBeUndefined();
      expect(response.headers()["content-range"]).toBeUndefined();
      expect(response.headers()["x-kombify-artifact-sha256"]).toBeUndefined();
      const body = await response.body();
      expect(body.length).toBeLessThan(1024);
      const error = JSON.parse(body.toString()).error;
      if (entry.status === 503) {
        expect(error.details.reason_code).toBe("render_downloads_forbidden");
      } else {
        expect(error.code).toBe("UNAUTHORIZED");
      }
    }
  }
  expect((await request.get(`${API_BASE}/api/v1/health`)).status()).toBe(200);
});

test("the Companion follows live wizard use-case choices", async ({ page }) => {
  test.skip(!roles.includes("cloud"), "the cloud runtime role is not selected");
  const bootstrapFailures: string[] = [];
  page.on("response", async (response) => {
    const pathname = new URL(response.url()).pathname;
    if (
      response.status() < 400 ||
      !/\/(homelab|features|use-cases)$/.test(pathname)
    )
      return;
    const body = await response.json().catch(() => undefined);
    const code = body?.error?.code ?? body?.code ?? body?.error;
    bootstrapFailures.push(
      `${pathname} HTTP ${response.status()}${
        typeof code === "string" && /^[a-zA-Z0-9_-]+$/.test(code)
          ? ` ${code}`
          : ""
      }`,
    );
  });
  page.on("requestfailed", (request) => {
    const pathname = new URL(request.url()).pathname;
    if (/\/(homelab|features|use-cases)$/.test(pathname)) {
      bootstrapFailures.push(`${pathname} ${request.failure()?.errorText}`);
    }
  });
  // Observe the real host -> panel message, without intercepting any product
  // API or retaining the panel's authentication messages.
  await page.addInitScript(() => {
    (window as ContextWindow).__wizardContexts = [];
    window.addEventListener("message", (event) => {
      const message = event.data;
      if (message?.type !== "kombify:panel-bridge") return;
      const envelope =
        message.kind === "context.set"
          ? message.payload
          : message.kind === "host.init"
            ? message.payload?.context
            : undefined;
      if (envelope?.context)
        (window as ContextWindow).__wizardContexts?.push(envelope.context);
    });
  });
  await authenticateRuntimeUser("cloud", page, RUNTIME_AUTH_OPTIONS);
  // The wizard itself, without the choice between existing and new targets.
  await page.goto("/stacks/new?target=new");
  await expect(page.getByTestId("creation-goals"))
    .toBeVisible()
    .catch((cause) => {
      throw new Error(
        `wizard bootstrap failed: ${bootstrapFailures.join("; ")}`,
        { cause },
      );
    });
  await page.locator("[data-kombify-companion-launcher]").click();
  const current = async () => {
    for (const frame of page.frames()) {
      const context = await frame
        .evaluate(() => (window as ContextWindow).__wizardContexts?.at(-1))
        .catch(() => undefined);
      if (context?.surface === "techstack") return context;
    }
    return undefined;
  };
  await expect
    .poll(async () => (await current())?.page?.facts?.wizardStep)
    .toBe("goals");
  const initial = await current();
  expect(initial?.route).toBe("/stacks/new");
  expect(initial?.page?.facts?.availableUseCases.split("+")).toEqual(
    expect.arrayContaining(["media", "files"]),
  );
  expect(initial?.page?.facts?.mediaOptions?.split("+")).toContain(
    "app:jellyfin",
  );
  expect(initial?.page?.facts?.filesOptions?.split("+")).toContain(
    "app:paperless-ngx",
  );
  // These choices remain in the tab's wizard draft. Never submit a run or
  // create infrastructure during this context regression journey. The card
  // toggle reads wizard.goals.add/remove followed by the card title.
  for (const name of ["Media Streaming", "Documents & Files"]) {
    const remove = page.getByRole("button", {
      name: `Remove from your kit ${name}`,
      exact: true,
    });
    const id = name === "Media Streaming" ? "media" : "files";
    if (await remove.isVisible()) await remove.click();
    await expect
      .poll(
        async () =>
          (await current())?.page?.facts?.selectedUseCases?.split("+") ?? [],
      )
      .not.toContain(id);
    await page
      .getByRole("button", { name: `Add to your kit ${name}`, exact: true })
      .click();
    await expect
      .poll(
        async () =>
          (await current())?.page?.facts?.selectedUseCases?.split("+") ?? [],
      )
      .toContain(id);
  }
  await expect
    .poll(
      async () =>
        (await current())?.page?.facts?.selectedUseCases?.split("+") ?? [],
    )
    .toEqual(expect.arrayContaining(["media", "files"]));
  await page.getByTestId("wizard-next").click();
  await expect
    .poll(async () => (await current())?.page?.facts?.wizardStep)
    .toBe("server");
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await page
    .getByRole("alertdialog")
    .getByRole("button", { name: "Leave page", exact: true })
    .click();
  await expect.poll(async () => (await current())?.route).toBe("/settings");
  expect((await current())?.page?.facts?.wizardStep).toBeUndefined();
});

test.describe.serial("Runtime Auth0 smoke", () => {
  for (const role of roles) {
    test(`${role} user can create a TechStack browser session`, async ({
      page,
    }) => {
      test.skip(STAGE_CONTEXT_JOURNEY && role !== "cloud", "The Stage A/B journey uses the canonical cloud users");
      const ownPropertyErrors: string[] = [];
      page.on("pageerror", (error) => {
        if (/Object\.hasOwn is not a function/.test(error.message)) {
          ownPropertyErrors.push(error.message);
        }
      });
      const previewRouting = NIGHTLY ? await observePreviewInitialRouting(page) : undefined;
      const auth = await authenticateRuntimeUser(
        role,
        page,
        { ...RUNTIME_AUTH_OPTIONS,
          ...(role !== "cloud" ? {}
            : STAGE_CONTEXT_JOURNEY ? { fixturePrefix: STAGE_COHORT_FIXTURE }
            : NIGHTLY ? { fixturePrefix: STAGE_PROPOSAL_FIXTURE } : {}) },
      );
      await previewRouting?.assertInitialRequest();
      expect(auth.token).toBeTruthy();
      await expect(page.getByText("Session Expired")).toHaveCount(0);
      if (
        !NIGHTLY &&
        process.env.TECHSTACK_RUNTIME_E2E_USE_CASE_PLACEMENT === "1"
      ) {
        test.setTimeout(300_000);
        await verifyUseCasePlacement(page);
      }
      const whoami = await fetchBrowserWhoAmI(page);
      expect(whoami.status).toBe(200);
      await test.step("the hosted control plane installs no host software for a signed-in person", () =>
        verifyHostInstallRefused(auth.token),
      );
      await test.step("a maintenance cancel on a server the person does not own answers one refusal", () =>
        verifyMaintenanceCancelRefusedOnce(auth.token),
      );
      if (role === "cloud") {
        if (INVENTORY_EXPECTATION && !STAGE_CONTEXT_JOURNEY) {
          assertCanonicalDemoPrincipal(whoami.body);
        }
        if (!PERSONAL_SETTINGS_CONTEXT_JOURNEY) await verifyTestTenantBoundary(auth.token, whoami.body);
        if (STAGE_CONTEXT_JOURNEY) {
          if (PERSONAL_SETTINGS_CONTEXT_JOURNEY) await verifyStagePersonalSettingsIsolation(page);
          else await verifyStageOnboardingContextIsolation(page);
          expect(ownPropertyErrors).toEqual([]);
          await previewRouting?.assertInitialRequest();
          return;
        }
        if (NIGHTLY) {
          // Desired-state writes use the public bearer's signed Stage context.
          // Provider execution retains its separate assigned-owner guard.
          const stacks = await authenticatedJSON(auth.token, "/api/v1/stacks");
          expect(stacks.status).toBe(200);
          await test.step("Owner action card uses the shared Gateway to fixed Preview", () =>
            verifyOwnerActionCardDecision(page, auth.token),
          );
          await verifyPreviewStackIsolation(page);
        } else {
          await verifyCloudInventoryCore(page, auth.token);
          await verifyConnectClientList(page);
        }
        if (EXPECTED_DATA_PROFILE) {
          await verifyDataProfile(page, EXPECTED_DATA_PROFILE);
        }
        await verifyExistingTargetsFirst(page, auth.token);
        await verifyCreationProcesses(page, auth.token);
        await page.goto("/stacks/new", { waitUntil: "domcontentloaded" });
        await openNewServerWizard(page);
        await expect(page).toHaveURL(/[?&]step=goals(&|$)/);
        const storage = page.getByTestId("easy-feature-storage");
        await expect(storage).toBeVisible();
        if ((await storage.getAttribute("data-plan")) === "available") {
          await storage
            .getByRole("button", {
              name: /^Add to your kit /,
              pressed: false,
            })
            .click();
        }
        await expect(storage).toHaveAttribute("data-plan", /^(included|required)$/);
        await page.getByTestId("wizard-next").click();
        await expect(page.getByTestId("easy-step-2")).toBeVisible();
        await verifyStepNavigation(page);
      }
      expect(ownPropertyErrors).toEqual([]);
      await previewRouting?.assertInitialRequest();
    });
  }

  test("cloud user can create a session through the Windows browser handoff login URL", async ({
    page,
  }) => {
    test.skip(STAGE_CONTEXT_JOURNEY, "The isolated Stage journey does not exercise native handoff");
    const identifierSubmissions: boolean[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (
        url.origin !== "https://login.kombify.io" ||
        url.pathname !== "/u/login/identifier" || request.method() !== "POST"
      ) return;
      const fields = new URLSearchParams(request.postData() ?? "");
      if (fields.has("username")) {
        identifierSubmissions.push(Boolean(fields.get("username")));
      }
    });
    const auth = await authenticateRuntimeUser("cloud", page, {
      ...RUNTIME_AUTH_OPTIONS,
      entryPath:
        "/api/v2/auth/login?return_to=%2Fstacks&client=windows&open_browser=1",
    });
    expect(identifierSubmissions).toContain(true);
    expect(identifierSubmissions).not.toContain(false);
    expect(auth.token).toBeTruthy();
    await expect(page.getByText("Session Expired")).toHaveCount(0);
    const whoami = await fetchBrowserWhoAmI(page);
    expect(whoami.status).toBe(200);
    if (INVENTORY_EXPECTATION) {
      assertCanonicalDemoPrincipal(whoami.body);
    }
  });
});

// The existing settings APIs run through the shared Gateway and real Stage
// persistence. Feature management stays with Edge/OpenFeature: /features is
// deliberately read-only, so this journey never invents a local flag writer.
async function verifyStagePersonalSettingsIsolation(page: Page) {
  const browser = page.context().browser();
  if (!browser) throw new Error("Personal settings isolation requires separate browser contexts");
  const own = await captureGatewayApiSession(page);
  expect(own.apiBase).toBe("https://api.kombify.io/v1/techstack");
  const secondContext = await browser.newContext({ baseURL: PRODUCT_BASE });
  try {
    const secondPage = await secondContext.newPage();
    const routing = await observePreviewInitialRouting(secondPage);
    await authenticateRuntimeUser("cloud", secondPage, {
      ...RUNTIME_AUTH_OPTIONS, fixturePrefix: "TEST_ALL_YOU_NEED_USER",
    });
    await routing.assertInitialRequest();
    const other = await captureGatewayApiSession(secondPage);
    expect(other.apiBase).toBe(own.apiBase);
    const subject = (token: string) => {
      const claims: unknown = JSON.parse(Buffer.from(token.split(".")[1] ?? "", "base64url").toString("utf8"));
      if (!isRecord(claims) || typeof claims.sub !== "string" || !claims.sub) throw new Error("Stage bearer omitted its subject");
      return claims.sub;
    };
    const ownSubject = subject(own.token);
    const otherSubject = subject(other.token);
    expect(ownSubject).not.toBe(otherSubject);
    const request = async (token: string, route: string, method = "GET", body?: unknown, forgedSubject?: string): Promise<Record<string, unknown>> => {
      const response = await fetch(`${own.apiBase}${route}`, {
        method, headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json",
          "x-kombify-release-channel": "preview",
          ...(forgedSubject ? { "x-user-id": forgedSubject, "x-org-id": `usr:${forgedSubject}` } : {}) },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }), signal: AbortSignal.timeout(15_000),
      });
      expect(response.status, `Stage personal settings ${method} ${route}`).toBe(200);
      const raw: unknown = await response.json();
      if (!isRecord(raw) || !isRecord(raw.data)) throw new Error("Stage personal settings returned an invalid response");
      return raw.data;
    };
    const disclosurePath = "/operator/self-disclosure";
    const value = (record: Record<string, unknown>) => {
      if ((record.source !== "techstack" && record.source !== "link") || !isRecord(record.disclosure)) {
        // There is no DELETE or legacy-cloud writer in this API. Refuse before
        // mutation unless the existing fixture can be restored exactly by PUT.
        throw new Error("Personal settings isolation requires an existing restorable disclosure for each canonical fixture");
      }
      return { source: record.source, disclosure: record.disclosure };
    };
    const profile = async (token: string) => {
      const result = await request(token, "/unifier/recommendations", "POST", {
        deployment_lane: "saas", surface: "easy", goals: ["files"], services: [],
      });
      if (!isRecord(result.decision_context) || !isRecord(result.decision_context.operator)) {
        throw new Error("The real Unifier omitted its operator evidence");
      }
      const operator = result.decision_context.operator;
      return { score: operator.score, source: operator.source, evidence: operator.evidence };
    };
    const featureStates = async (token: string, forgedSubject?: string) => {
      const result = await request(token, "/features", "GET", undefined, forgedSubject);
      return ["security", "beta", "ux"].map((category) => {
        const states = result[category];
        if (!Array.isArray(states) || states.length === 0) throw new Error("The Stage feature reader omitted its effective preferences/consents");
        return states.map((state: unknown) => {
          if (!isRecord(state) || typeof state.key !== "string" || typeof state.enabled !== "boolean" || typeof state.has_consent !== "boolean") {
            throw new Error("The Stage feature reader returned an invalid preference/consent state");
          }
          return { key: state.key, enabled: state.enabled, has_consent: state.has_consent };
        }).sort((left, right) => left.key.localeCompare(right.key));
      });
    };
    const ownBefore = value(await request(own.token, disclosurePath));
    const otherBefore = value(await request(other.token, disclosurePath));
    const ownProfile = await profile(own.token);
    const otherProfile = await profile(other.token);
    const ownFeatures = await featureStates(own.token);
    const otherFeatures = await featureStates(other.token);
    const ownExperience = ownBefore.disclosure.experience === "expert" ? "techie" : "expert";
    const otherExperience = otherBefore.disclosure.experience === "guided" ? "hands-off" : "guided";
    const ownChange = { source: ownBefore.source, disclosure: { ...ownBefore.disclosure, experience: ownExperience } };
    const otherChange = { source: otherBefore.source, disclosure: { ...otherBefore.disclosure, experience: otherExperience } };
    const restores: Array<() => Promise<void>> = [];
    const replace = async (token: string, previous: typeof ownBefore, next: typeof ownChange, forgedSubject: string) => {
      // Register restoration before sending a write: a lost response may still
      // have committed. Conditional PUT never overwrites a concurrent edit.
      restores.push(async () => {
        const observed = value(await request(token, disclosurePath));
        if (isDeepStrictEqual(observed, previous)) return;
        if (!isDeepStrictEqual(observed, next)) throw new Error("The Stage disclosure changed concurrently; conditional restoration refused");
        await request(token, disclosurePath, "PUT", { ...previous, if_unchanged: next });
        expect(value(await request(token, disclosurePath))).toEqual(previous);
      });
      const updated = await request(token, disclosurePath, "PUT", { ...next, if_unchanged: previous }, forgedSubject);
      expect(value(updated)).toEqual(next);
      expect(value(await request(token, disclosurePath))).toEqual(next);
      expect(isRecord(updated.profile)).toBe(true);
      const derived = await profile(token);
      expect(derived.source).toBe("self-disclosure");
      expect(derived.score).toBe(isRecord(updated.profile) ? updated.profile.score : undefined);
      expect(derived.evidence).toEqual(expect.arrayContaining([expect.objectContaining({ signal: `self-disclosure:experience:${next.disclosure.experience}` })]));
    };
    try {
      await replace(own.token, ownBefore, ownChange, otherSubject);
      expect(value(await request(other.token, disclosurePath, "GET", undefined, ownSubject))).toEqual(otherBefore);
      expect(await profile(other.token)).toEqual(otherProfile);
      await replace(other.token, otherBefore, otherChange, ownSubject);
      expect(value(await request(own.token, disclosurePath, "GET", undefined, otherSubject))).toEqual(ownChange);
      expect(await featureStates(own.token, otherSubject)).toEqual(ownFeatures);
      expect(await featureStates(other.token, ownSubject)).toEqual(otherFeatures);
      const anonymous = await fetch(`${own.apiBase}${disclosurePath}`, {
        headers: { "x-kombify-release-channel": "preview" }, signal: AbortSignal.timeout(15_000),
      });
      expect([401, 403]).toContain(anonymous.status);
    } finally {
      const restoration = await Promise.allSettled(restores.reverse().map((restore) => restore()));
      if (restoration.some((result) => result.status === "rejected")) throw new Error("Stage personal settings restoration/readback failed; inspect the canonical fixture before retrying");
    }
    expect(await profile(own.token)).toEqual(ownProfile);
    expect(await profile(other.token)).toEqual(otherProfile);
  } finally {
    await secondContext.close();
  }
}

// Stack isolation on kombify-stg-pg (platform-ld78c.5): two canonical users
// on the shared Gateway. The other user cannot read, list or change the first
// user's stack, a forged identity header changes nothing and anonymous is
// refused. The write probe carries a body the owner's own validation refuses,
// so even a broken boundary mutates nothing and there is nothing to clean up.
async function verifyPreviewStackIsolation(page: Page) {
  const browser = page.context().browser();
  if (!browser) throw new Error("Stack isolation requires separate browser contexts");
  const own = await captureGatewayApiSession(page);
  expect(new URL(own.apiBase).origin).toBe("https://api.kombify.io");
  const secondContext = await browser.newContext({ baseURL: PRODUCT_BASE });
  try {
    const secondPage = await secondContext.newPage();
    await authenticateRuntimeUser("cloud", secondPage, {
      ...RUNTIME_AUTH_OPTIONS, fixturePrefix: STAGE_COHORT_FIXTURE,
    });
    const other = await captureGatewayApiSession(secondPage);
    expect(other.apiBase).toBe(own.apiBase);
    const subject = (token: string) => {
      const claims: unknown = JSON.parse(Buffer.from(token.split(".")[1] ?? "", "base64url").toString("utf8"));
      if (!isRecord(claims) || typeof claims.sub !== "string" || !claims.sub) throw new Error("Gateway bearer omitted its subject");
      return claims.sub;
    };
    const ownSubject = subject(own.token);
    expect(subject(other.token)).not.toBe(ownSubject);
    const call = async (token: string | undefined, route: string, method = "GET", forgedSubject?: string) => {
      const response = await fetch(`${own.apiBase}${route}`, {
        method,
        headers: { "Content-Type": "application/json", "x-kombify-release-channel": "preview",
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
          ...(forgedSubject ? { "x-user-id": forgedSubject, "x-org-id": `usr:${forgedSubject}` } : {}) },
        ...(method === "PUT" ? { body: JSON.stringify({ mode: "isolation-probe" }) } : {}),
        signal: AbortSignal.timeout(15_000),
      });
      const raw: unknown = await response.json().catch(() => null);
      return { status: response.status, data: isRecord(raw) ? raw.data : undefined };
    };
    const stackIDs = async (token: string, forgedSubject?: string) => {
      const listed = await call(token, "/stacks", "GET", forgedSubject);
      expect(listed.status, "Stage stack list").toBe(200);
      if (!Array.isArray(listed.data)) throw new Error("Stage stack list returned an invalid response");
      return listed.data.map((stack: unknown) => (isRecord(stack) ? String(stack.id ?? "") : ""));
    };
    // The write probe stops at validation, so no live Execution host is needed.
    const ownStacks = await stackIDs(own.token);
    const stackID = ownStacks[0];
    if (!stackID) throw new Error("The canonical Stage fixture has no stack to isolate");
    const ownBefore = await call(own.token, `/stacks/${stackID}`);
    expect(ownBefore.status, "owner stack read").toBe(200);
    // The owner's mutation passes the Stage guard and reaches validation (422);
    // the other user must stop at 404.
    expect((await call(own.token, `/stacks/${stackID}/routing`, "PUT")).status, "owner invalid routing write").toBe(422);
    expect((await call(other.token, `/stacks/${stackID}`)).status, "other user stack read").toBe(404);
    expect((await call(other.token, `/stacks/${stackID}`, "GET", ownSubject)).status, "forged-subject stack read").toBe(404);
    expect((await call(other.token, `/stacks/${stackID}/routing`, "PUT", ownSubject)).status, "other user routing write").toBe(404);
    expect(await stackIDs(other.token)).not.toContain(stackID);
    expect(await stackIDs(other.token, ownSubject)).not.toContain(stackID);
    expect([401, 403]).toContain((await call(undefined, `/stacks/${stackID}`)).status);
    const ownAfter = await call(own.token, `/stacks/${stackID}`);
    expect(ownAfter.status).toBe(200);
    expect(ownAfter.data).toEqual(ownBefore.data);
    expect(await stackIDs(own.token)).toEqual(ownStacks);
  } finally {
    await secondContext.close();
  }
}

// Extends the real signed-user smoke; no API/DB is replaced. The explicit
// selection admits only the fixed managed Stage product through shared Gateway.
async function verifyStageOnboardingContextIsolation(page: import("@playwright/test").Page) {
  expect(NIGHTLY).toBe(true);
  expect(new URL(PRODUCT_BASE).origin).toBe("https://preview.techstack.kombify.io");
  const browser = page.context().browser();
  if (!browser) throw new Error("The Stage A/B journey requires isolated browser contexts");
  const ownSession = await captureGatewayApiSession(page);
  expect(ownSession.apiBase).toBe("https://api.kombify.io/v1/techstack");
  const secondContext = await browser.newContext({ baseURL: PRODUCT_BASE });
  try {
    const secondPage = await secondContext.newPage();
    const previewRouting = await observePreviewInitialRouting(secondPage);
    await authenticateRuntimeUser("cloud", secondPage, {
      ...RUNTIME_AUTH_OPTIONS, fixturePrefix: "TEST_ALL_YOU_NEED_USER"
    });
    await previewRouting.assertInitialRequest();
    const otherSession = await captureGatewayApiSession(secondPage);
    expect(otherSession.apiBase).toBe(ownSession.apiBase);
    const subject = (token: string) => {
      const value: unknown = JSON.parse(Buffer.from(token.split(".")[1] ?? "", "base64url").toString("utf8"));
      if (!isRecord(value) || typeof value.sub !== "string" || !value.sub) throw new Error("Gateway bearer omitted its subject");
      return value.sub;
    };
    const ownSubject = subject(ownSession.token);
    const otherSubject = subject(otherSession.token);
    expect(ownSubject).not.toBe(otherSubject);
    const request = async (token: string, method = "GET", action?: string, revision?: number, forgedSubject?: string) => {
      const response = await fetch(`${ownSession.apiBase}/onboarding/techstack.platform`, {
        method,
        headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json",
          "x-kombify-release-channel": "preview",
          ...(revision === undefined ? {} : { "If-Match": String(revision) }),
          ...(forgedSubject ? { "x-user-id": forgedSubject, "x-org-id": `usr:${forgedSubject}` } : {}) },
        ...(action ? { body: JSON.stringify({ action }) } : {}), signal: AbortSignal.timeout(15_000)
      });
      expect(response.status, `Stage onboarding ${method}`).toBe(200);
      const raw: unknown = await response.json();
      if (!isRecord(raw) || !isRecord(raw.data) || !isRecord(raw.data.state) || typeof raw.data.revision !== "number") {
        throw new Error("Stage onboarding returned an invalid persisted state");
      }
      return { revision: raw.data.revision, state: raw.data.state };
    };
    const retained = (state: Record<string, unknown>) => ({ completed: state.completed, seen: state.seen_steps, status: state.status });
    const before = await request(ownSession.token);
    const otherBefore = await request(otherSession.token);
    expect(["active", "dismissed"]).toContain(before.state.status);
    const action = before.state.status === "dismissed" ? "resume" : "dismiss";
    let changed = false;
    try {
      const updated = await request(ownSession.token, "PUT", action, before.revision, otherSubject);
      changed = true;
      expect(updated.revision).toBeGreaterThan(before.revision);
      expect(updated.state.completed).toEqual(before.state.completed);
      expect(updated.state.seen_steps).toEqual(before.state.seen_steps);
      const ownRead = await request(ownSession.token);
      expect(ownRead.revision).toBe(updated.revision);
      expect(retained(ownRead.state)).toEqual(retained(updated.state));
      const otherAfter = await request(otherSession.token, "GET", undefined, undefined, ownSubject);
      expect(otherAfter.revision).toBe(otherBefore.revision);
      expect(retained(otherAfter.state)).toEqual(retained(otherBefore.state));
    } finally {
      // Restore dismissal through its supported action, retaining completions.
      if (changed) await request(ownSession.token, "PUT", before.state.status === "dismissed" ? "dismiss" : "resume");
    }
  } finally {
    await secondContext.close();
  }
}

// Observe the real bootstrap and first signed-in data request. An unexpected
// Stable request is stopped before reaching persistence; no response is mocked.
async function observePreviewInitialRouting(page: Page) {
  const gateway = new URL(GATEWAY_TECHSTACK_BASE);
  const expected = process.env.TECHSTACK_E2E_EXPECTED_GATEWAY_SHA?.trim() ?? "";
  if (gateway.origin !== "https://api.kombify.io" || !/^[a-f0-9]{40}$/.test(expected)) {
    throw new Error("Preview routing requires the qualified shared Gateway source SHA before a user journey");
  }
  const assertSource = async () => {
    const health = await fetch(`${gateway.origin}/gateway/health`, {
      redirect: "error", headers: { "Cache-Control": "no-store" }, signal: AbortSignal.timeout(10_000),
    });
    const release = await health.json();
    if (health.status !== 200 || release.release?.sha !== expected || release.release_channel !== "stable") {
      throw new Error("The shared Gateway does not serve the qualified stable source");
    }
  };
  await assertSource();
  let bootstrapReturned = false;
  let bootstrapChannel = "";
  const initial: { afterBootstrap: boolean; channel: string }[] = [];
  page.on("response", async (response) => {
    const url = new URL(response.url());
    if (url.origin !== new URL(PRODUCT_BASE).origin || url.pathname !== "/api/v1/client/bootstrap") return;
    bootstrapReturned = response.status() === 200;
    const body = await response.json().catch(() => ({}));
    bootstrapChannel = (body.data ?? body).release_channel ?? "";
  });
  await page.route((url) => url.origin === gateway.origin &&
    (url.pathname === gateway.pathname || url.pathname.startsWith(`${gateway.pathname}/`)), async (route) => {
    const channel = route.request().headers()["x-kombify-release-channel"] ?? "";
    initial.push({ afterBootstrap: bootstrapReturned, channel });
    if (!bootstrapReturned || channel !== "preview") {
      await route.abort("blockedbyclient");
      return;
    }
    await route.continue();
  });
  return { async assertInitialRequest() {
    await expect.poll(() => initial.length).toBeGreaterThan(0);
    expect(bootstrapChannel).toBe("preview");
    expect(initial.every((request) => request.afterBootstrap && request.channel === "preview")).toBe(true);
    await assertSource();
  } };
}

// Cloud's use-case handoff opens the creation wizard with the use case
// selected; the owner's existing servers are offered on its Server step,
// next to a new server. A chosen server that runs a StackKit takes the use
// case through it with its verified distribution. There is no separate target
// page and no "Change target" control. Real owner reads and planner responses;
// no API or browser routes are replaced.
async function verifyUseCasePlacement(page: import("@playwright/test").Page) {
  const plans: Array<{ stack: string; goals: string[]; body: any }> = [];
  const observe = async (response: import("@playwright/test").Response) => {
    if (
      !new URL(response.url()).pathname.endsWith(
        "/api/v1/unifier/layout/recommendations",
      )
    )
      return;
    if (!response.ok()) return;
    const request = response.request().postDataJSON();
    const body = await response.json();
    plans.push({
      stack: request.stack_id,
      goals: request.goals,
      body: body.data ?? body,
    });
  };
  page.on("response", observe);
  try {
    await page.goto("/stacks/new?use-case=vault&setup=backend:vaultwarden", {
      waitUntil: "domcontentloaded",
    });
    await expect(page.getByTestId("easy-step-1")).toBeVisible({
      timeout: 180_000,
    });
    await expect(page.getByTestId("use-case-target-back")).toHaveCount(0);
    // One homelab per person: the sidebar adds use cases, never a homelab.
    await expect(page.getByRole("link", { name: "Add use cases" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Create homelab" })).toHaveCount(0);
    await expect(
      page
        .getByTestId("creation-goals")
        .locator('[aria-pressed="true"]')
        .first(),
    ).toBeVisible();
    await page.getByTestId("wizard-next").click();
    const homelab = await openServerChoice(page);
    if (homelab) {
      const chosen = homelab.locator(
        '[data-testid="homelab-server"]:has(input:checked)',
      );
      if (await chosen.count()) {
        await expect(page).toHaveURL(/[?&]target=server(%3A|:)/);
        await expect(page.getByTestId("server-provisioning-step")).toHaveCount(
          0,
        );
        const stackkit = (await chosen.getAttribute("data-has-stackkit")) === "true";
        if (stackkit) {
          await expect
            .poll(() =>
              plans.some(
                (entry) =>
                  entry.goals.length === 1 && entry.goals[0] === "vault",
              ),
            )
            .toBe(true);
          for (const id of [3, 4, 5])
            await expect(page.getByTestId(`wizard-step-${id}`)).toHaveAttribute(
              "data-state",
              "inactive",
            );
        }
      }
      // A new server stays one click away on the same step.
      await page.getByTestId("homelab-server-new").click();
      await expect(page).toHaveURL(/[?&]target=new(&|$)/);
    }
    await expect(page.getByTestId("server-provisioning-step")).toBeVisible();
    await expect(page.getByTestId("wizard-step-3")).not.toHaveAttribute(
      "data-state",
      "inactive",
    );
    await expect(page).toHaveURL(
      /use-case=vault.*setup=backend%3Avaultwarden|use-case=vault.*setup=backend:vaultwarden/,
    );
  } finally {
    page.off("response", observe);
  }
}

// The Server step once the owner's servers are read: their homelab's servers
// with a new server beside them, or, without any, the new server alone.
// Returns the homelab region when the owner has servers.
async function openServerChoice(page: import("@playwright/test").Page) {
  await expect(page.getByTestId("easy-step-2")).toBeVisible();
  const homelab = page.getByTestId("homelab-servers");
  await expect(
    page
      .locator(
        '[data-testid="homelab-servers"] [role="radiogroup"], [data-testid="server-provisioning-step"]',
      )
      .first(),
  ).toBeVisible({ timeout: 180_000 });
  if (!(await homelab.count())) return null;
  await expect(homelab.locator('input[type="radio"]:checked')).toHaveCount(1);
  return homelab;
}

// Use cases a person adds start in the same wizard: one use case is chosen,
// then the Server step names the servers of their homelab.
async function selectStorageAndOpenServerStep(
  page: import("@playwright/test").Page,
) {
  await expect(page.getByTestId("easy-step-1")).toBeVisible({
    timeout: 180_000,
  });
  const storage = page.getByTestId("easy-feature-storage");
  await expect(storage).toBeVisible();
  if ((await storage.getAttribute("data-plan")) === "available") {
    await storage
      .getByRole("button", { name: /^Add to your kit /, pressed: false })
      .click();
  }
  await page.getByTestId("wizard-next").click();
  return openServerChoice(page);
}

async function openNewServerWizard(page: import("@playwright/test").Page) {
  // The wizard opens at once; existing servers are a choice on its Server step.
  await expect(page.getByTestId("easy-wizard")).toBeVisible({
    timeout: 180_000,
  });
  await expect(page.getByTestId("use-case-target-back")).toHaveCount(0);
}

// Use cases go onto the owner's existing servers before a new one is offered
// (kombify-Techstack-8m9s.1): the dashboard offers "Add use cases" with any
// number of StackKits, and on the Server step a server that runs a StackKit
// is chosen first, which leaves Access, Users and Login with that StackKit.
// Real owner reads only; nothing is submitted.
async function verifyExistingTargetsFirst(
  page: import("@playwright/test").Page,
  token: string,
) {
  const listed = await authenticatedJSON(token, "/api/v1/stacks");
  const rows = listed.body?.data ?? listed.body;
  const stacks = { count: Array.isArray(rows) ? rows.length : 0 };
  if (stacks.count > 0) {
    await page.goto("/dashboard", { waitUntil: "domcontentloaded" });
    await expect(page.getByTestId("add-use-cases-button")).toBeVisible({
      timeout: 60_000,
    });
  }
  await page.goto("/use-cases/new", { waitUntil: "domcontentloaded" });
  await expect(page.getByTestId("use-case-target-back")).toHaveCount(0);
  const homelab = await selectStorageAndOpenServerStep(page);
  if (!homelab) {
    expect(stacks.count).toBe(0);
    return;
  }
  const ready = homelab.locator(
    '[data-testid="homelab-server"][data-state="ready"][data-has-stackkit="true"]',
  );
  if ((await ready.count()) === 0) return;
  await ready.first().click();
  await expect(page).toHaveURL(/[?&]target=server(%3A|:)/);
  for (const id of [3, 4, 5])
    await expect(page.getByTestId(`wizard-step-${id}`)).toHaveAttribute(
      "data-state",
      "inactive",
    );
  // The choice survives a reload.
  await page.reload({ waitUntil: "domcontentloaded" });
  await expect(page.getByTestId("easy-step-2")).toBeVisible({ timeout: 60_000 });
  await expect(
    page.locator('[data-testid="homelab-server"]:has(input:checked)'),
  ).toHaveCount(1, { timeout: 180_000 });
}

// Each creation process shows only the steps it can change (kombify-Techstack-8m9s.3
// and .4): adding a server asks for the use cases and the server, a new StackKit
// from that page also for access and users, and the homelab settings act on Users
// and Login. A server the owner started from that cannot be used is named instead
// of silently replaced. Real owner reads only; nothing is submitted.
async function verifyCreationProcesses(
  page: import("@playwright/test").Page,
  token: string,
) {
  const stepState = (id: number, state: string) =>
    expect(page.getByTestId(`wizard-step-${id}`)).toHaveAttribute(
      "data-state",
      state,
    );
  await page.goto("/use-cases/new?server=no-such-server", {
    waitUntil: "domcontentloaded",
  });
  if (await selectStorageAndOpenServerStep(page))
    await expect(page.getByTestId("use-case-preferred-skipped")).toBeVisible();

  const listed = await authenticatedJSON(token, "/api/v1/stacks");
  const rows = listed.body?.data ?? listed.body;
  const stackId = Array.isArray(rows) ? rows[0]?.id : undefined;
  if (!stackId) return;

  await page.goto(`/stacks/${encodeURIComponent(stackId)}/servers/new`, {
    waitUntil: "domcontentloaded",
  });
  await expect(page.getByTestId("easy-step-1")).toBeVisible({ timeout: 60_000 });
  await stepState(1, "current");
  for (const id of [3, 4, 5]) await stepState(id, "inactive");
  await page.getByTestId("wizard-next").click();
  await expect(page.getByTestId("easy-step-2")).toBeVisible();
  await page.locator(".advanced-trigger").click();
  await page
    .locator("button[aria-pressed]", { hasText: /New StackKit/i })
    .click();
  for (const id of [3, 4]) await stepState(id, "upcoming");
  await stepState(5, "inactive");

  // The module a host frames answers on its own route: Cloud's popup opens it.
  await page.goto(
    "/embed/creation?embedded=true&host_navigation=true&process=add-use-case&frame=sheet&use-case=vault",
    { waitUntil: "domcontentloaded" },
  );
  await expect(page.getByTestId("creation-embed")).toHaveAttribute(
    "data-process",
    "add-use-case",
    { timeout: 60_000 },
  );
  await expect(page.getByTestId("easy-wizard")).toBeVisible({
    timeout: 180_000,
  });
  await expect(page.getByTestId("use-case-target-back")).toHaveCount(0);

  await page.goto("/settings/homelab", { waitUntil: "domcontentloaded" });
  const settingsEmpty = page.getByTestId("homelab-settings-empty");
  await expect(
    page.getByTestId("homelab-settings-users").or(settingsEmpty),
  ).toBeVisible({ timeout: 60_000 });
  if (await settingsEmpty.isVisible()) return;
  for (const id of [1, 2, 3]) await stepState(id, "inactive");
  await page.getByTestId("wizard-next").click();
  await expect(page.getByTestId("homelab-settings-login")).toBeVisible();
  await page.getByTestId("wizard-step-4-select").click();
  await expect(page.getByTestId("homelab-settings-users")).toBeVisible();
}

// Steps already reached open from the stepper and from the browser history,
// the step is in the address and survives a reload (kombify-Techstack-8m9s.2).
async function verifyStepNavigation(page: import("@playwright/test").Page) {
  const current = (id: number) =>
    expect(page.getByTestId(`wizard-step-${id}`)).toHaveAttribute(
      "data-state",
      "current",
    );
  await expect(page).toHaveURL(/[?&]step=server(&|$)/);
  await expect(page.getByTestId("wizard-step-3-select")).toHaveCount(0);
  // A step already reached says in one line what it holds.
  await expect(page.getByTestId("wizard-step-1-summary")).not.toBeEmpty();
  await page.getByTestId("wizard-step-1-select").click();
  await current(1);
  await expect(page.getByTestId("easy-step-1")).toBeVisible();
  await expect(page).toHaveURL(/[?&]step=goals(&|$)/);
  await page.goBack();
  await current(2);
  await expect(page.getByTestId("easy-step-2")).toBeVisible();
  await page.goBack();
  await current(1);
  await page.goForward();
  await current(2);
  await expect(page.getByTestId("easy-step-2")).toBeVisible();
  await expect(page.getByText(/Leave this page\?/)).toHaveCount(0);
  await page.reload({ waitUntil: "domcontentloaded" });
  await expect(page.getByTestId("easy-step-2")).toBeVisible({ timeout: 60_000 });
  await current(2);
}

// A missing Box route must leave the existing Connect clients and counts intact
// (platform-ezugz.4). Observe both real Gateway reads; no API replacement.
async function verifyConnectClientList(page: import("@playwright/test").Page) {
  const [clients, edges, servers] = await Promise.all([
    page.waitForResponse(
      (response) => new URL(response.url()).pathname === "/v1/connect/devices",
      { timeout: 20_000 },
    ),
    page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname === "/v1/connect/edge-devices",
      { timeout: 20_000 },
    ),
    page.waitForResponse((response) => isUIAPIURL(response.url(), "servers"), {
      timeout: 20_000,
    }),
    page.goto("/servers", { waitUntil: "domcontentloaded" }),
  ]);
  if (!clients.ok()) return;
  const body = await clients.json();
  const devices = body.devices as { device_id: string; display_name: string }[];
  expect(Array.isArray(devices)).toBe(true);
  const section = page.locator("section[aria-labelledby='nodes-devices']");
  for (const device of devices) {
    await expect(
      section.getByText(device.display_name, { exact: true }).first(),
    ).toBeVisible();
  }
  if ([403, 404, 503].includes(edges.status())) {
    await expect(
      section.getByText("Box devices are unavailable here.", { exact: false }),
    ).toHaveCount(0);
    await expect(
      section.getByText("Could not refresh Box devices", { exact: false }),
    ).toHaveCount(0);
    if (devices.length === 0) {
      await expect(page.getByTestId("nodes-devices-state")).toBeVisible();
    }
    // When the server source answered too, its summary remains visible.
    if (servers.ok()) {
      await expect(page.getByTestId("nodes-summary")).toBeVisible();
    }
  }
}

// The prepared owner creates and declines a proposal on their own server.
// Missing approval stops at Gateway; an unissued envelope reaches Techstack's
// own card-approval check without approving the proposal or starting a workflow.
async function verifyOwnerActionCardDecision(page: Page, privateToken: string) {
  // The product session can read this owner's inventory, but Stage mutations
  // require the public bearer and the Gateway's verified origin context.
  expect(new URL(PRODUCT_BASE).origin).toBe(
    "https://preview.techstack.kombify.io",
  );
  expect(API_BASE).toBe(PRODUCT_BASE);
  const gateway = await captureGatewayApiSession(page);
  qualifyStageGatewaySession(gateway, STAGE_PROPOSAL_FIXTURE);
  const expectedSHA =
    process.env.TECHSTACK_E2E_EXPECTED_GATEWAY_SHA?.trim() ?? "";
  expect(expectedSHA).toMatch(/^[a-f0-9]{40}$/);
  const gatewayHealth = await fetch(
    new URL("/gateway/health", gateway.apiBase),
    {
      cache: "no-store",
      redirect: "error",
      signal: AbortSignal.timeout(15_000),
    },
  );
  expect(gatewayHealth.status).toBe(200);
  const health = requireRecord(
    await gatewayHealth.json(),
    "shared Gateway health",
  );
  expect(health.release_channel).toBe("stable");
  expect(health.release?.sha).toBe(expectedSHA);
  test
    .info()
    .annotations.push({
      type: "ril-shared-gateway-source",
      description: expectedSHA,
    });
  const serverResponse = await gatewayRequest(gateway, "/v1/techstack/servers");
  const stackResponse = await gatewayRequest(gateway, "/v1/techstack/stacks");
  expect(serverResponse.status, serverResponse.diagnostic).toBe(200);
  expect(stackResponse.status, stackResponse.diagnostic).toBe(200);
  const servers = canonicalRows(serverResponse.body, "servers");
  // The card names the server's stack, or the owner's first stack for a
  // server that is not in one yet.
  const server =
    servers.find((row) => String(row.techstack_id ?? "").trim()) ?? servers[0];
  const stackID =
    String(server?.techstack_id ?? "").trim() ||
    (uiStackIDs(stackResponse.body)[0] ?? "");
  expect(
    server && stackID,
    `the fixture owner needs one server and one stack; observed=${JSON.stringify(serverDiagnostic(servers))}`,
  ).toBeTruthy();
  const run = `${Date.now()}-${Math.random().toString(16).slice(2, 10)}`;
  const proposal = {
    server_id: String(server?.id).trim(),
    title: "Journey check: owner action on their own server",
    action: { stack_id: stackID },
  };
  const browser = page.context().browser();
  if (!browser)
    throw new Error("RIL isolation requires separate browser contexts");
  const secondContext = await browser.newContext({ baseURL: PRODUCT_BASE });
  const pending: Array<{
    gateway: RuntimeGatewaySession;
    id: string;
    created: boolean;
  }> = [];
  const failures: unknown[] = [];
  const create = async (
    session: RuntimeGatewaySession,
    body: typeof proposal & { tenantId?: string },
    cleanupSession = session,
  ) => {
    const id = randomUUID();
    // Register before sending: a transport failure can hide a persisted create.
    const target: (typeof pending)[number] = { gateway: cleanupSession, id, created: false };
    pending.push(target);
    const result = await gatewayRequest(session, "/v1/ril/action-cards", {
      ...body,
      id,
    });
    if (result.status === 201) {
      target.created = true;
      const observedID = String(result.body?.data?.id ?? "").trim();
      if (observedID) target.id = observedID;
      expect(observedID).toBe(id);
    }
    return { ...result, id };
  };
  try {
    const direct = await create(
      { token: privateToken, apiBase: API_BASE },
      proposal,
      gateway,
    );
    expect(direct.status, direct.diagnostic).toBe(403);
    expect(direct.reason).toBe("stage_owner_not_admitted");
    const anonymous = await create(
      { ...gateway, token: "" },
      proposal,
      gateway,
    );
    expect(anonymous.status, anonymous.diagnostic).toBe(401);
    const foreign = await create(gateway, {
      ...proposal,
      tenantId: "org_journey_foreign",
    });
    expect(foreign.status, foreign.diagnostic).toBe(403);
    expect(foreign.reason).toBe("access_denied");
    const created = await create(gateway, proposal);
    expect(created.status, created.diagnostic).toBe(201);
    const cardPath = `/v1/ril/action-cards/${created.id}`;
    const ownRead = await gatewayRequest(gateway, cardPath);
    expect(ownRead.status, ownRead.diagnostic).toBe(200);
    const executed = await gatewayRequest(gateway, `${cardPath}/execute`, {
      execution_id: `journey-execution-${run}`,
      trace_id: `journey-trace-${run}`,
      idempotency_key: `journey-idempotency-${run}`,
    });
    expect(executed.status, executed.diagnostic).toBe(409);
    expect(executed.reason).toBe("approval_required");
    expect(
      executed.body?.service,
      "missing approval stops at Gateway RIL preflight",
    ).toBe("ril");
    const secondPage = await secondContext.newPage();
    const routing = await observePreviewInitialRouting(secondPage);
    await authenticateRuntimeUser("cloud", secondPage, {
      ...RUNTIME_AUTH_OPTIONS,
      fixturePrefix: STAGE_COHORT_FIXTURE,
    });
    await routing.assertInitialRequest();
    const other = await captureGatewayApiSession(secondPage);
    qualifyStageGatewaySession(other, STAGE_COHORT_FIXTURE);
    // Both prepared cohort users may propose; execution stays with its assigned owner.
    const otherStacks = await gatewayRequest(other, "/v1/techstack/stacks");
    const otherServers = await gatewayRequest(other, "/v1/techstack/servers");
    expect(otherStacks.status, otherStacks.diagnostic).toBe(200);
    expect(otherServers.status, otherServers.diagnostic).toBe(200);
    const otherServer = canonicalRows(otherServers.body, "servers")[0];
    const otherStack = uiStackIDs(otherStacks.body)[0];
    if (!otherServer || !otherStack)
      throw new Error(
        "The second Stage fixture needs its own server and stack",
      );
    const otherCard = await create(other, {
      ...proposal,
      server_id: String(otherServer.id),
      action: { stack_id: otherStack },
    });
    expect(otherCard.status, otherCard.diagnostic).toBe(201);
    const otherPath = `/v1/ril/action-cards/${otherCard.id}`;
    const otherRead = await gatewayRequest(other, otherPath);
    expect(otherRead.status, otherRead.diagnostic).toBe(200);
    expect(otherRead.body?.data?.status).toMatch(/^awaiting_(approval|grant)$/);
    const upstreamExecute = await gatewayRequest(other, `${otherPath}/execute`, {
      execution_id: `journey-upstream-execution-${run}`,
      trace_id: `journey-upstream-trace-${run}`,
      idempotency_key: `journey-upstream-idempotency-${run}`,
      approval: {
        id: `journey-unissued-${run}`,
        token: `journey-unissued-${run}`,
        status: "approved",
      },
    });
    expect(upstreamExecute.status, upstreamExecute.diagnostic).toBe(409);
    expect(
      upstreamExecute.body?.error?.details?.reason_code,
      "Techstack's own card-approval check must answer",
    ).toBe("approval_required");
    expect(
      (await gatewayRequest(gateway, otherPath)).status,
      "owner cannot read other caller's card",
    ).toBe(404);
    expect(
      (await gatewayRequest(other, cardPath)).status,
      "other caller cannot read owner's card",
    ).toBe(404);
    expect((await gatewayRequest(gateway, cardPath)).body?.data).toEqual(
      ownRead.body?.data,
    );
    expect((await gatewayRequest(other, otherPath)).body?.data).toEqual(
      otherRead.body?.data,
    );
  } catch (error) {
    failures.push(error);
  } finally {
    for (const target of pending) {
      try {
        const cardPath = `/v1/ril/action-cards/${target.id}`;
        const found = await gatewayRequest(target.gateway, cardPath);
        if (found.status === 404) {
          expect(
            target.created,
            "an accepted card must remain visible to its cleanup owner",
          ).toBe(false);
          continue;
        }
        expect(found.status, found.diagnostic).toBe(200);
        const denied = await gatewayRequest(
          target.gateway,
          `${cardPath}/deny`,
          {
            audit_correlation_id: `journey-deny-${run}-${target.id}`,
          },
        );
        expect(denied.status, denied.diagnostic).toBe(200);
        expect(denied.body?.data?.status).toBe("denied");
        const persisted = await gatewayRequest(target.gateway, cardPath);
        expect(persisted.status, persisted.diagnostic).toBe(200);
        expect(persisted.body?.data?.status).toBe("denied");
      } catch (error) {
        failures.push(error);
      }
    }
    await secondContext.close().catch((error: unknown) => failures.push(error));
  }
  if (failures.length)
    throw new AggregateError(
      failures,
      "RIL journey or proposal cleanup failed",
    );
}

function qualifyStageGatewaySession(
  gateway: RuntimeGatewaySession,
  prefix: "TEST_ALL_YOU_NEED_USER" | "TEST_RIL_PRO_USER",
) {
  expect(gateway.apiBase).toBe("https://api.kombify.io/v1/techstack");
  const subject = process.env[`${prefix}_ID`]?.trim();
  if (!subject)
    throw new Error(`Canonical Stage subject ${prefix} is not configured`);
  const claims = requireRecord(
    JSON.parse(
      Buffer.from(gateway.token.split(".")[1] ?? "", "base64url").toString(
        "utf8",
      ),
    ),
    "captured public bearer",
  );
  expect(claims.sub).toBe(subject);
  expect(claims.iss).toBe("https://login.kombify.io/");
  expect(Array.isArray(claims.aud) ? claims.aud : [claims.aud]).toContain(
    "https://api.kombify.io",
  );
  expect(
    claims.org_id === undefined || claims.org_id === `usr:${subject}`,
  ).toBe(true);
  expect(claims.act).toBeUndefined();
}

async function gatewayRequest(
  gateway: RuntimeGatewaySession,
  endpoint: string,
  body?: unknown,
) {
  // RIL keeps its public /v1/ril path; /v1/techstack maps to /api/v1 instead.
  const response = await fetch(new URL(endpoint, gateway.apiBase), {
    redirect: "error",
    method: body === undefined ? "GET" : "POST",
    headers: {
      Accept: "application/json",
      ...(gateway.token ? { Authorization: `Bearer ${gateway.token}` } : {}),
      "Content-Type": "application/json",
      "x-kombify-release-channel": "preview",
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    signal: AbortSignal.timeout(15_000),
  });
  const text = await response.text();
  let parsed: any;
  try {
    parsed = text ? JSON.parse(text) : null;
  } catch {
    throw new Error(
      `${endpoint} returned invalid JSON (HTTP ${response.status})`,
    );
  }
  const reason = String(
    parsed?.error?.details?.reason_code ??
      parsed?.reason_code ??
      parsed?.reason ??
      parsed?.error?.code ??
      "",
  ).trim();
  test.info().annotations.push({
    type: "ril-request",
    description: JSON.stringify({
      origin: new URL(gateway.apiBase).origin,
      endpoint,
      method: body === undefined ? "GET" : "POST",
      channel: "preview",
      status: response.status,
      reason,
      requestId: response.headers.get("x-request-id"),
    }),
  });
  return {
    status: response.status,
    body: parsed,
    reason,
    diagnostic: `${endpoint} HTTP ${response.status} reason=${reason || "none"} request_id=${response.headers.get("x-request-id") ?? ""}`,
  };
}

async function verifyCloudInventoryCore(
  page: Parameters<typeof authenticateRuntimeUser>[1],
  token: string,
) {
  // The general live Auth0 smoke follows the canonical runtime authorities.
  // The older exact local-anchor profile retains its REST/MCP compatibility
  // proof below until that fixture is migrated independently.
  if (!INVENTORY_EXPECTATION) {
    await verifyCanonicalCloudRuntimeCore(page, token);
    return;
  }
  const servers = await authenticatedJSON(token, "/api/v1/inventory/servers");
  const services = await authenticatedJSON(token, "/api/v1/inventory/services");
  const initialize = await authenticatedJSON(token, "/api/v1/mcp", {
    method: "POST",
    headers: {
      Accept: "application/json, text/event-stream",
      "MCP-Protocol-Version": "2025-11-25",
    },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: "p1-initialize",
      method: "initialize",
      params: {
        protocolVersion: "2025-11-25",
        capabilities: {},
        clientInfo: { name: "techstack-p1-gate", version: "1" },
      },
    }),
  });
  const tools = await authenticatedJSON(token, "/api/v1/mcp", {
    method: "POST",
    headers: {
      Accept: "application/json, text/event-stream",
      "MCP-Protocol-Version": "2025-11-25",
    },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: "p1-tools",
      method: "tools/list",
      params: {},
    }),
  });
  const listServers = await authenticatedJSON(token, "/api/v1/mcp", {
    method: "POST",
    headers: {
      Accept: "application/json, text/event-stream",
      "MCP-Protocol-Version": "2025-11-25",
    },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: "p1-list-servers",
      method: "tools/call",
      params: { name: "list_servers", arguments: {} },
    }),
  });
  const listServices = await authenticatedJSON(token, "/api/v1/mcp", {
    method: "POST",
    headers: {
      Accept: "application/json, text/event-stream",
      "MCP-Protocol-Version": "2025-11-25",
    },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: "p1-list-services",
      method: "tools/call",
      params: { name: "list_services", arguments: {} },
    }),
  });

  const actualTools = Array.isArray(tools.body?.result?.tools)
    ? tools.body.result.tools
        .map((tool: { name?: unknown }) => String(tool?.name ?? "").trim())
        .filter(Boolean)
        .sort()
    : [];
  expect(actualTools).toEqual(INVENTORY_TOOLS);
  expect(initialize.body?.result?.protocolVersion).toBe("2025-11-25");
  expect(listServers.body?.result?.isError).not.toBe(true);
  expect(listServices.body?.result?.isError).not.toBe(true);

  const restServerIDs = inventoryIDs(servers.body, "servers", true);
  const restServiceIDs = inventoryIDs(services.body, "services", true);
  const exactInventory = INVENTORY_EXPECTATION
    ? assertRuntimeInventoryExpectation(
        INVENTORY_EXPECTATION,
        servers.body,
        services.body,
      )
    : null;
  expect(
    restServerIDs.length,
    "P1 inventory proof requires at least one real REST server",
  ).toBeGreaterThan(0);
  expect(
    restServiceIDs.length,
    "P1 inventory proof requires at least one real REST service",
  ).toBeGreaterThan(0);
  const mcpServerIDs = inventoryIDs(
    listServers.body?.result?.structuredContent,
    "servers",
    false,
  );
  const mcpServiceIDs = inventoryIDs(
    listServices.body?.result?.structuredContent,
    "services",
    false,
  );
  expect(mcpServerIDs).toEqual(restServerIDs);
  expect(mcpServiceIDs).toEqual(restServiceIDs);
  if (INVENTORY_EXPECTATION && exactInventory) {
    const exactMCPInventory = assertRuntimeInventoryExpectation(
      INVENTORY_EXPECTATION,
      listServers.body?.result?.structuredContent,
      listServices.body?.result?.structuredContent,
    );
    expect(mcpServerIDs).toEqual(exactInventory.serverIds);
    expect(mcpServiceIDs).toEqual(exactInventory.serviceIds);
    expect(exactMCPInventory.semantic).toEqual(exactInventory.semantic);
  }
  expect(
    mcpServerIDs.length,
    "P1 inventory proof requires at least one MCP server",
  ).toBeGreaterThan(0);
  expect(
    mcpServiceIDs.length,
    "P1 inventory proof requires at least one MCP service",
  ).toBeGreaterThan(0);

  const artifactsDir = path.resolve(
    process.env.RUNTIME_E2E_ARTIFACTS_DIR ?? "../artifacts/runtime-e2e",
  );
  await mkdir(artifactsDir, { recursive: true });
  const stackOperationStatuses: number[] = [];
  page.on("response", (response) => {
    if (isStackOperationsURL(response.url())) {
      stackOperationStatuses.push(response.status());
    }
  });
  const stacksResponsePromise = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      isUIAPIURL(response.url(), "stacks"),
    { timeout: 20_000 },
  );
  await page.goto("/dashboard", { waitUntil: "domcontentloaded" });
  const stacksResponse = await stacksResponsePromise;
  assertCandidateUIResponseOrigin(stacksResponse.url());
  expect(stacksResponse.ok()).toBe(true);
  const stackIDs = uiStackIDs(await stacksResponse.json());
  await expect(page.getByTestId("stacks-dashboard")).toBeVisible();
  if (stackIDs.length > 0) {
    await expect(page.getByTestId("stack-action-bar")).toBeVisible();
    await expect
      .poll(() => stackOperationStatuses.some(isHTTP2xx), { timeout: 20_000 })
      .toBe(true);
  }
  await expect(page.getByTestId("stacks-error-panel")).toHaveCount(0);
  // Unattested: the dashboard leg's proof is the assertions above. The image
  // is kept for triage only, so it must not carry the inventory artifact name
  // the gate hashes against the evidence.
  await writeFile(
    path.join(artifactsDir, "p1-dashboard.png"),
    await page.screenshot({ fullPage: true }),
  );

  // The canonical server inventory is an observation surface, not a dashboard
  // control: the dashboard renders the servers the operator acts on, and the
  // authoritative record (with its REST/DOM parity proof) lives on /monitoring.
  const monitoringInventoryResponsePromise = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      new URL(response.url()).pathname.endsWith("/api/v1/servers"),
    { timeout: 20_000 },
  );
  await page.goto("/monitoring", { waitUntil: "domcontentloaded" });
  const monitoringInventoryResponse = await monitoringInventoryResponsePromise;
  assertCandidateUIResponseOrigin(monitoringInventoryResponse.url());
  expect(monitoringInventoryResponse.ok()).toBe(true);
  await expect(page.getByTestId("monitoring-section-right-now")).toBeVisible();
  await expect(
    page.getByTestId("monitoring-inventory-unavailable"),
  ).toHaveCount(0);
  // Parity is asserted against the authoritative REST record above, so the
  // page cannot pass by rendering a stale or partial population of its own.
  await expect(page.getByTestId("monitoring-node-summary")).toHaveCount(
    restServerIDs.length,
  );
  const dashboardDOMServerIDs = await domInventoryIDs(
    page,
    "monitoring-node-summary",
    "data-server-id",
  );
  expect(dashboardDOMServerIDs).toEqual(restServerIDs);
  expect(
    dashboardDOMServerIDs.length,
    "P1 inventory proof requires a canonical server card",
  ).toBeGreaterThan(0);
  if (INVENTORY_EXPECTATION && exactInventory) {
    await assertExpectedDashboardInventory(page, INVENTORY_EXPECTATION, exactInventory);
  }
  const inventoryScreenshot = await page.screenshot({ fullPage: true });
  await writeFile(
    path.join(artifactsDir, "p1-inventory-monitoring.png"),
    inventoryScreenshot,
  );

  const servicesResponsePromise = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      isUIAPIURL(response.url(), "registry/services"),
    { timeout: 20_000 },
  );
  const servicesInventoryResponsePromise = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      isUIAPIURL(response.url(), "inventory/services"),
    { timeout: 20_000 },
  );
  await page.goto("/services", { waitUntil: "domcontentloaded" });
  const [servicesResponse, servicesInventoryResponse] = await Promise.all([
    servicesResponsePromise,
    servicesInventoryResponsePromise,
  ]);
  assertCandidateUIResponseOrigin(servicesResponse.url());
  assertCandidateUIResponseOrigin(servicesInventoryResponse.url());
  expect(servicesResponse.ok()).toBe(true);
  expect(servicesInventoryResponse.ok()).toBe(true);
  const registryServiceIDs = uiRegistryServiceIDs(
    await servicesResponse.json(),
  );
  const uiServiceIDs = inventoryIDs(
    await servicesInventoryResponse.json(),
    "services",
    true,
  );
  expect(uiServiceIDs).toEqual(restServiceIDs);
  await expect(page.getByTestId("services-page")).toBeVisible();
  await expect(page.getByTestId("runtime-service-card")).toHaveCount(
    uiServiceIDs.length,
  );
  await expect(page.getByTestId("runtime-service-list")).toBeVisible();
  await expect(page.getByTestId("inventory-services-error-panel")).toHaveCount(
    0,
  );
  const registryWarningCount = await page
    .getByTestId("registry-services-warning")
    .count();
  const registryManagementAction = page.getByTestId("new-service-button");
  await expect(registryManagementAction).toBeEnabled({ timeout: 10_000 });
  const registryManagementActionEnabled =
    await registryManagementAction.isEnabled();
  expect(registryWarningCount).toBe(0);
  expect(registryManagementActionEnabled).toBe(true);
  const servicesDOMIDs = await domInventoryIDs(
    page,
    "runtime-service-card",
    "data-service-id",
  );
  expect(servicesDOMIDs).toEqual(restServiceIDs);
  expect(
    servicesDOMIDs.length,
    "P1 inventory proof requires a canonical service card",
  ).toBeGreaterThan(0);
  if (INVENTORY_EXPECTATION && exactInventory) {
    await assertExpectedServicesInventory(page, exactInventory);
  }
  const servicesScreenshot = await page.screenshot({ fullPage: true });
  await writeFile(
    path.join(artifactsDir, "p1-inventory-services.png"),
    servicesScreenshot,
  );

  const evidence = {
    schema_version: 1,
    kind: "techstack-p1-inventory-core",
    result: "PASS",
    head_sha: process.env.GITHUB_SHA ?? "",
    origin: PRODUCT_BASE,
    expectation: {
      profile: INVENTORY_EXPECTATION?.profile ?? null,
      exact_fixture_required: Boolean(INVENTORY_EXPECTATION),
      exact_fixture_verified: Boolean(exactInventory),
    },
    rest: {
      servers_status: servers.status,
      services_status: services.status,
      response_shape_valid: true,
      server_count: restServerIDs.length,
      service_count: restServiceIDs.length,
      server_ids_sha256: idsSha256(restServerIDs),
      service_ids_sha256: idsSha256(restServiceIDs),
    },
    mcp: {
      initialize_status: initialize.status,
      protocol_version: initialize.body?.result?.protocolVersion,
      tools_list_status: tools.status,
      actual_tools: actualTools,
      list_servers_status: listServers.status,
      list_servers_is_error: listServers.body?.result?.isError,
      list_services_status: listServices.status,
      list_services_is_error: listServices.body?.result?.isError,
      response_shape_valid: true,
      server_count: mcpServerIDs.length,
      service_count: mcpServiceIDs.length,
      server_ids_sha256: idsSha256(mcpServerIDs),
      service_ids_sha256: idsSha256(mcpServiceIDs),
      server_ids_match_rest: true,
      service_ids_match_rest: true,
      server_count_matches_rest: true,
      service_count_matches_rest: true,
      semantic_fields_match_rest: Boolean(exactInventory),
    },
    ui: {
      dashboard: {
        // The canonical inventory proof moved to /monitoring; the dashboard
        // surface is still exercised above (rendered, no error panel).
        path: "/monitoring",
        rendered: true,
        backend_origin: new URL(monitoringInventoryResponse.url()).origin,
        backend_status: monitoringInventoryResponse.status(),
        backend_response_shape_valid: true,
        backend_item_count: restServerIDs.length,
        backend_ids_sha256: idsSha256(restServerIDs),
        dom_item_count: dashboardDOMServerIDs.length,
        dom_ids_sha256: idsSha256(dashboardDOMServerIDs),
        dom_ids_match_backend: true,
        dom_count_matches_backend: true,
        legacy_stacks_status: stacksResponse.status(),
        stack_operations_status: stackOperationStatuses.find(isHTTP2xx) ?? null,
        expected_state: "data",
        state_matches_backend: true,
        error_panel_absent: true,
        screenshot_sha256: sha256(inventoryScreenshot),
      },
      services: {
        path: "/services",
        rendered: true,
        backend_origin: new URL(servicesInventoryResponse.url()).origin,
        backend_status: servicesInventoryResponse.status(),
        backend_response_shape_valid: true,
        backend_item_count: uiServiceIDs.length,
        backend_ids_sha256: idsSha256(uiServiceIDs),
        dom_item_count: servicesDOMIDs.length,
        dom_ids_sha256: idsSha256(servicesDOMIDs),
        dom_ids_match_backend: true,
        dom_count_matches_backend: true,
        registry_backend_status: servicesResponse.status(),
        registry_response_shape_valid: true,
        registry_item_count: registryServiceIDs.length,
        registry_warning_absent: registryWarningCount === 0,
        registry_management_action_enabled: registryManagementActionEnabled,
        expected_state: "data",
        state_matches_backend: true,
        error_panel_absent: true,
        screenshot_sha256: sha256(servicesScreenshot),
        registry_backend_ok: servicesResponse.ok(),
      },
    },
    verified_at: new Date().toISOString(),
  };
  await writeFile(
    path.join(artifactsDir, "inventory-core-evidence.json"),
    `${JSON.stringify(evidence, null, 2)}\n`,
    "utf8",
  );
}

async function verifyCanonicalCloudRuntimeCore(
  page: Parameters<typeof authenticateRuntimeUser>[1],
  token: string,
) {
  const servers = await authenticatedJSON(token, "/api/v1/servers");
  const services = await authenticatedJSON(token, "/api/v1/services");
  const cockpit = await authenticatedJSON(token, "/api/v1/monitor/cockpit");
  const serverRows = canonicalRows(servers.body, "servers");
  const serviceRows = canonicalRows(services.body, "services");
  const restServerIDs = canonicalRowIDs(serverRows, "servers");
  const restServiceIDs = canonicalRowIDs(serviceRows, "services");
  const expectedServerRef = String(
    process.env.TECHSTACK_RUNTIME_E2E_EXPECTED_SERVER_ID ?? "",
  ).trim();
  const expectedServiceCount = Number(
    process.env.TECHSTACK_RUNTIME_E2E_EXPECTED_SERVICE_COUNT ?? "0",
  );
  const expectedServer = expectedServerRef
    ? serverRows.find((candidate) =>
        [candidate.id, candidate.name]
          .map((value) => String(value ?? "").trim())
          .includes(expectedServerRef),
      )
    : undefined;
  const cockpitData = requireRecord(
    requireRecord(cockpit.body, "cockpit response").data,
    "cockpit data",
  );
  const selectedTechstackID = String(
    expectedServer?.techstack_id ?? cockpitData.techstack_id ?? "",
  ).trim();

  expect(
    restServerIDs.length,
    "Canonical runtime proof requires at least one real server",
  ).toBeGreaterThan(0);
  if (expectedServerRef) {
    expect(
      expectedServer,
      `Canonical servers omitted id/name ${expectedServerRef}; observed=${JSON.stringify(serverDiagnostic(serverRows))}`,
    ).toBeTruthy();
    expect(String(expectedServer?.environment_class ?? "").toLowerCase()).toBe(
      "cloud",
    );
    expect(String(expectedServer?.offering ?? "").toLowerCase()).toBe(
      "external_vps",
    );
    expect(String(expectedServer?.provider_id ?? "").toLowerCase()).toBe(
      "hostinger",
    );

    const expectedServerID = String(expectedServer?.id ?? "").trim();
    const serverServices = serviceRows.filter(
      (service) => String(service.server_id ?? "").trim() === expectedServerID,
    );
    if (Number.isInteger(expectedServiceCount) && expectedServiceCount > 0) {
      const cockpit = await authenticatedJSON(
        token,
        `/api/v1/monitor/cockpit?techstack_id=${encodeURIComponent(String(expectedServer?.techstack_id ?? "").trim())}`,
      );
      expect(
        serverServices,
        `Canonical service authority omitted ${expectedServerRef}; cockpit diagnostic=${JSON.stringify(cockpitServiceDiagnostic(cockpit.body))}`,
      ).toHaveLength(expectedServiceCount);
    }
    for (const service of serverServices) {
      expect(service.target_kind).toBe("server");
      expect(service.management_state).toBe("observed");
      expect(String(service.desired_state ?? "").trim()).not.toBe("");
      expect(String(service.observed_state ?? "").trim()).not.toBe("");
      expect(
        String(
          requireRecord(service.health, "service health").state ?? "",
        ).trim(),
      ).not.toBe("");
      expect(
        [
          service.desired_state,
          service.observed_state,
          requireRecord(service.health, "service health").state,
        ].map((value) => String(value ?? "").toLowerCase()),
      ).not.toContain("migrating");
    }
  }

  const artifactsDir = path.resolve(
    process.env.RUNTIME_E2E_ARTIFACTS_DIR ?? "../artifacts/runtime-e2e",
  );
  await mkdir(artifactsDir, { recursive: true });

  const techstackQuery = selectedTechstackID
    ? `?techstack_id=${encodeURIComponent(selectedTechstackID)}`
    : "";
  await page.goto("/dashboard", {
    waitUntil: "domcontentloaded",
  });
  await expect(page.getByTestId("stacks-dashboard")).toBeVisible();
  await expect(page.getByTestId("stacks-error-panel")).toHaveCount(0);
  if (expectedServiceCount > 0) {
    await expect(page.getByTestId("stack-running-services-kpi")).toContainText(
      `${expectedServiceCount} recorded`,
    );
  }
  await writeFile(
    path.join(artifactsDir, "canonical-dashboard.png"),
    await page.screenshot({ fullPage: true }),
  );

  const monitoringServersResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      isUIAPIURL(response.url(), "servers"),
    { timeout: 20_000 },
  );
  await page.goto(`/monitoring${techstackQuery}`, {
    waitUntil: "domcontentloaded",
  });
  const monitoringResponse = await monitoringServersResponse;
  expect(monitoringResponse.ok()).toBe(true);
  const monitoringAPIRows = canonicalRows(
    await monitoringResponse.json(),
    "servers",
  );
  const monitoringAPIIDs = canonicalRowIDs(monitoringAPIRows, "servers");
  await expect(
    page.getByTestId("monitoring-inventory-unavailable"),
  ).toHaveCount(0);
  await expect(page.getByTestId("monitoring-node-summary")).toHaveCount(
    monitoringAPIIDs.length,
  );
  const monitoringDOMIDs = await domInventoryIDs(
    page,
    "monitoring-node-summary",
    "data-server-id",
  );
  expect(monitoringDOMIDs).toEqual(monitoringAPIIDs);
  if (expectedServer) {
    const expectedServerID = String(expectedServer.id ?? "").trim();
    await expect(
      page.locator(
        `[data-testid="monitoring-node-summary"][data-server-id="${expectedServerID}"]`,
      ),
    ).toHaveCount(1);
    // Node identity lives on the node's own page; the fleet view carries the
    // three axes and the id it is addressable by.
    await page.goto(`/monitoring/${encodeURIComponent(expectedServerID)}`, {
      waitUntil: "domcontentloaded",
    });
    const identity = page.getByTestId("server-monitoring-identity");
    await expect(identity).toBeVisible({ timeout: 20_000 });
    await expect(identity).toContainText(/cloud/i);
    await expect(identity).toContainText(/external vps/i);
    await expect(identity).toContainText(/hostinger/i);
    await page.goto(`/monitoring${techstackQuery}`, {
      waitUntil: "domcontentloaded",
    });
    await expect(page.getByTestId("monitoring-node-summary")).toHaveCount(
      monitoringAPIIDs.length,
    );
  }
  await writeFile(
    path.join(artifactsDir, "canonical-monitoring.png"),
    await page.screenshot({ fullPage: true }),
  );

  // Leave the previous page first: a late read of its own would match the wait
  // below and lose its body when the navigation commits.
  await page.goto("about:blank");
  // The client replays a 401 or 429 once, so a failed first answer is not the
  // one the page renders.
  const servicesResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      response.ok() &&
      isUIAPIURL(response.url(), "services"),
    { timeout: 20_000 },
  );
  const applicationsResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      response.ok() &&
      isUIAPIURL(response.url(), "service-applications"),
    { timeout: 20_000 },
  );
  await page.goto("/services", { waitUntil: "domcontentloaded" });
  const [servicesUIResponse, applicationsUIResponse] = await Promise.all([
    servicesResponse,
    applicationsResponse,
  ]);
  const servicesAPIIDs = canonicalRowIDs(
    canonicalRows(await servicesUIResponse.json(), "services"),
    "services",
  );
  const applications = canonicalRows(
    await applicationsUIResponse.json(),
    "service-applications",
  ).filter((application) => application.system !== true);
  // The list renders only after the inventory loaded; every count below would
  // otherwise pass on an empty page.
  await expect(page.getByTestId("runtime-service-list")).toBeVisible();
  await expect(page.getByTestId("inventory-services-error-panel")).toHaveCount(
    0,
  );
  await expect(page.getByTestId("inventory-rows-rejected")).toHaveCount(0);
  // The page shows one card per application, so its services are components of
  // that card. A service no application claims has to show as a system service,
  // or it would be on no card at all.
  await expect(page.getByTestId("runtime-service-card")).toHaveCount(
    applications.length,
  );
  const applicationDOMIDs = await domInventoryIDs(
    page,
    "runtime-service-card",
    "data-service-id",
  );
  expect(applicationDOMIDs).toEqual(
    applications.map((application) => String(application.application_id)).sort(),
  );
  const componentIDs = new Set<string>(
    applications.flatMap((application) =>
      (application.components ?? []).map((component: { id: unknown }) =>
        String(component.id),
      ),
    ),
  );
  const systemDOMIDs = await domInventoryIDs(
    page,
    "system-service-card",
    "data-service-id",
  );
  expect(systemDOMIDs.filter((id) => componentIDs.has(id))).toEqual([]);
  expect(systemDOMIDs.filter((id) => !servicesAPIIDs.includes(id))).toEqual([]);
  expect(
    servicesAPIIDs.filter(
      (id) => !componentIDs.has(id) && !systemDOMIDs.includes(id),
    ),
  ).toEqual([]);
  expect(servicesAPIIDs).toEqual(restServiceIDs);
  await writeFile(
    path.join(artifactsDir, "canonical-services.png"),
    await page.screenshot({ fullPage: true }),
  );

  await writeFile(
    path.join(artifactsDir, "canonical-runtime-evidence.json"),
    `${JSON.stringify(
      {
        schema_version: 1,
        kind: "techstack-canonical-runtime-live",
        result: "PASS",
        head_sha: process.env.GITHUB_SHA ?? "",
        origin: PRODUCT_BASE,
        server_count: restServerIDs.length,
        service_count: restServiceIDs.length,
        server_ids: restServerIDs,
        service_ids_sha256: idsSha256(restServiceIDs),
        expected_server_ref: expectedServerRef || null,
        resolved_server_id: expectedServer
          ? String(expectedServer.id ?? "").trim()
          : null,
        expected_service_count: expectedServiceCount || null,
        dashboard_matches_api: true,
        monitoring_techstack_id: selectedTechstackID || null,
        monitoring_server_count: monitoringAPIIDs.length,
        monitoring_matches_api: true,
        services_matches_api: true,
        verified_at: new Date().toISOString(),
      },
      null,
      2,
    )}\n`,
    "utf8",
  );
}

function cockpitServiceDiagnostic(body: unknown) {
  const root = requireRecord(body, "cockpit response");
  const data = requireRecord(root.data, "cockpit data");
  const services = Array.isArray(data.services) ? data.services : [];
  return {
    techstack_id: String(data.techstack_id ?? "").trim(),
    count: services.length,
    services: services.map((service, index) => {
      const row = requireRecord(service, `cockpit services[${index}]`);
      return {
        id: String(row.id ?? "").trim(),
        name: String(row.name ?? "").trim(),
        status: String(row.status ?? "").trim(),
        target_server_id: String(row.target_server_id ?? "").trim(),
      };
    }),
  };
}

function serverDiagnostic(rows: Record<string, any>[]) {
  return rows.map((row) => ({
    id: String(row.id ?? "").trim(),
    name: String(row.name ?? "").trim(),
    techstack_id: String(row.techstack_id ?? "").trim(),
    environment_class: String(row.environment_class ?? "").trim(),
    offering: String(row.offering ?? "").trim(),
    provider_id: String(row.provider_id ?? "").trim(),
  }));
}

/**
 * The monitoring start page renders the three orthogonal axes per node, not
 * the node's identity facts: address, domain, OS and StackKit come from the
 * stack operations payload and are asserted on the stack server detail page,
 * which is the surface that still reports them.
 */
// The ids come from the REST read-back of this run, never from the profile.
async function assertExpectedDashboardInventory(
  page: Parameters<typeof authenticateRuntimeUser>[1],
  expectation: RuntimeInventoryExpectation,
  readBack: RuntimeInventoryAssertionResult,
) {
  const card = page.locator(
    `[data-testid="monitoring-node-summary"][data-server-id="${readBack.semantic.server.id}"]`,
  );
  await expect(card).toHaveCount(1);
  const axes = card.getByTestId("monitoring-node-axis");
  await expect(axes).toHaveCount(3);
  await expect(axes.filter({ hasText: /./ })).toHaveCount(3);
  await expect(card.locator('[data-axis="Conn"]')).toHaveText(
    new RegExp(escapeRegExp(expectation.server.connection), "i"),
  );
  await expect(card.locator('[data-axis="Life"]')).toHaveText(
    new RegExp(escapeRegExp(expectation.server.lifecycle), "i"),
  );
  await expect(card.locator('[data-axis="Health"]')).toHaveText(
    new RegExp(escapeRegExp(expectation.server.health), "i"),
  );
}

async function assertExpectedServicesInventory(
  page: Parameters<typeof authenticateRuntimeUser>[1],
  readBack: RuntimeInventoryAssertionResult,
) {
  await expect(page.getByTestId("runtime-service-card")).toHaveCount(
    readBack.serviceIds.length,
  );
  for (const service of readBack.semantic.services) {
    const card = page.locator(
      `[data-testid="runtime-service-card"][data-service-id="${service.id}"]`,
    );
    await expect(card).toHaveCount(1);
    await expect(
      card.getByText(new RegExp(`^${escapeRegExp(service.health)}$`, "i")),
    ).toBeVisible();
    await expect(card.locator("article")).toHaveCount(1);
    await expect(
      card.getByRole("button", { name: "Open", exact: true }),
    ).toBeVisible();
  }
}

async function verifyDataProfile(
  page: import("@playwright/test").Page,
  expected: string,
) {
  const gateway = await captureGatewayApiSession(page);
  if (NIGHTLY) qualifyStageGatewaySession(gateway, STAGE_PROPOSAL_FIXTURE);
  const response = await fetch(`${gateway.apiBase}/data-profile`, {
    redirect: "error",
    headers: {
      Accept: "application/json",
      Authorization: `Bearer ${gateway.token}`,
      ...(NIGHTLY ? { "x-kombify-release-channel": "preview" } : {}),
    },
    signal: AbortSignal.timeout(15_000),
  });
  const body = (await response.json().catch(() => null)) as {
    data?: Record<string, string>;
    reason_code?: string;
    error?: { code?: string };
  } | null;
  if (expected === "off") {
    expect(response.status, "verification off answers 404").toBe(404);
    expect(JSON.stringify(body)).toContain("not active on this deployment");
    return;
  }
  expect(
    response.status,
    `data profile read: ${body?.reason_code ?? body?.error?.code ?? ""}`,
  ).toBe(200);
  const axes = new Set(expected === "standard" ? [] : expected.split("+"));
  expect(body?.data).toMatchObject({
    res: axes.has("eu") ? "eu" : "global",
    ret: axes.has("zero") ? "zero" : "standard",
    loc: "hosted",
    source: "edge",
  });
  test.info().annotations.push({
    type: "stage-data-profile",
    description: JSON.stringify({
      origin: new URL(gateway.apiBase).origin,
      channel: NIGHTLY ? "preview" : "stable",
      status: response.status,
      res: body?.data?.res,
      ret: body?.data?.ret,
      loc: body?.data?.loc,
      source: body?.data?.source,
    }),
  });
  // The account menu shows the same profile.
  await page.goto("/dashboard");
  await page
    .locator('[data-onboarding-anchor="user-menu"] button')
    .first()
    .click();
  const status = page.getByTestId("data-profile-status");
  await expect(status).toBeVisible();
  await expect(status).toHaveAttribute(
    "data-profile-res",
    body?.data?.res ?? "",
  );
  await expect(status).toHaveAttribute(
    "data-profile-ret",
    body?.data?.ret ?? "",
  );
}

// Both host dependency routes refuse every signed-in person on a hosted
// edition (platform-vijkk). The edge deny answers before the runtime, and a
// Preview mutation admission may refuse the install first; the status read
// passes no mutation gate, so its runtime answer must name the hosted
// refusal. The body names no real dependency, so even a regression installs
// nothing.
async function verifyHostInstallRefused(token: string) {
  for (const [method, endpoint] of [
    ["GET", "/api/v1/autoinstall/status"],
    ["POST", "/api/v1/autoinstall/install"],
  ] as const) {
    const response = await fetch(`${API_BASE}${endpoint}`, {
      method,
      redirect: "error",
      headers: {
        Accept: "application/json",
        Authorization: `Bearer ${token}`,
        ...(NIGHTLY ? { "x-kombify-release-channel": "preview" } : {}),
        ...(method === "POST" ? { "Content-Type": "application/json" } : {}),
      },
      body: method === "POST" ? JSON.stringify({ dependencies: ["__host_install_refusal_probe__"] }) : undefined,
      signal: AbortSignal.timeout(15_000),
    });
    const text = await response.text();
    expect(response.status, `${method} ${endpoint}`).toBe(403);
    if (method !== "GET" || !(response.headers.get("content-type") ?? "").includes("json")) continue;
    const body = JSON.parse(text);
    expect(body?.error?.details?.reason_code, `${method} ${endpoint}`).toBe("hosted_control_plane");
  }
}

// A maintenance cancel for a server the person does not own is refused once.
// It used to write its 404 and then read the nil server, so a second
// "internal server error" envelope followed the 404 in the same body
// (platform-7r1il). The body must be exactly one JSON refusal.
async function verifyMaintenanceCancelRefusedOnce(token: string) {
  const endpoint = "/api/v1/servers/srv-journey-not-owned/actions/job-journey-not-owned/cancel";
  const response = await fetch(`${API_BASE}${endpoint}`, {
    method: "POST",
    redirect: "error",
    headers: {
      Accept: "application/json",
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
      ...(NIGHTLY ? { "x-kombify-release-channel": "preview" } : {}),
    },
    body: "{}",
    signal: AbortSignal.timeout(15_000),
  });
  const text = await response.text();
  expect([403, 404], endpoint).toContain(response.status);
  let body: { error?: { code?: string } } | undefined;
  expect(() => { body = JSON.parse(text); }, `${endpoint} answers one JSON refusal`).not.toThrow();
  if (response.status === 404) expect(body?.error?.code, endpoint).toBe("NOT_FOUND");
  // Only a person with the maintenance entitlement reaches the ownership check
  // that wrote the second body; a 403 here says the fixture lacks it.
  if (response.status === 403) test.info().annotations.push({ type: "maintenance-cancel", description: "entitlement refusal; ownership check not reached" });
}

async function authenticatedJSON(
  token: string,
  endpoint: string,
  init: RequestInit = {},
) {
  const response = await fetch(`${API_BASE}${endpoint}`, {
    ...init,
    redirect: "error",
    headers: {
      Accept: "application/json",
      Authorization: `Bearer ${token}`,
      ...(NIGHTLY ? { "x-kombify-release-channel": "preview" } : {}),
      ...(init.body ? { "Content-Type": "application/json" } : {}),
      ...(init.headers ?? {}),
    },
    signal: AbortSignal.timeout(15_000),
  });
  const text = await response.text();
  let body: any;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    throw new Error(`${endpoint} returned invalid JSON`);
  }
  if (!response.ok) {
    const diagnostic = safeAPIErrorDiagnostic(body);
    throw new Error(
      `${endpoint} failed with HTTP ${response.status}${diagnostic ? ` (${diagnostic})` : ""}`,
    );
  }
  return { status: response.status, body };
}

function safeAPIErrorDiagnostic(body: unknown) {
  if (!isRecord(body)) return "";
  const data = isRecord(body.data) ? body.data : null;
  const error = isRecord(body.error) ? body.error : null;
  const values = [
    body.reason_code,
    data?.reason_code,
    error?.reason_code,
    body.message,
    error?.message,
  ];
  return values
    .filter(
      (value): value is string =>
        typeof value === "string" && value.trim().length > 0,
    )
    .map((value) => value.trim().slice(0, 160))
    .filter((value, index, all) => all.indexOf(value) === index)
    .join(": ");
}

function inventoryIDs(
  body: unknown,
  collection: "servers" | "services",
  enveloped: boolean,
) {
  const root = requireRecord(body, `${collection} response`);
  const payload = enveloped
    ? requireRecord(root.data, `${collection} data envelope`)
    : root;
  if (!Array.isArray(payload[collection])) {
    throw new Error(`${collection} response omitted ${collection} array`);
  }
  if (
    typeof payload.observed_at !== "string" ||
    typeof payload.inventory_revision !== "number" ||
    !isRecord(payload.freshness)
  ) {
    throw new Error(`${collection} response has an invalid inventory shape`);
  }
  const ids = payload[collection].map((item, index) => {
    const record = requireRecord(item, `${collection}[${index}]`);
    const id = String(record.id ?? "").trim();
    if (!id) throw new Error(`${collection}[${index}] omitted id`);
    return id;
  });
  if (new Set(ids).size !== ids.length) {
    throw new Error(`${collection} response contains duplicate ids`);
  }
  return ids.sort();
}

function canonicalRows(
  body: unknown,
  collection: "servers" | "services" | "service-applications",
): Record<string, any>[] {
  const root = requireRecord(body, `${collection} canonical response`);
  if (!Array.isArray(root.data)) {
    throw new Error(`${collection} canonical response omitted data array`);
  }
  return root.data.map((item, index) =>
    requireRecord(item, `${collection}[${index}]`),
  );
}

function canonicalRowIDs(
  rows: Record<string, any>[],
  collection: "servers" | "services",
): string[] {
  const ids = rows.map((row, index) => {
    const id = String(row.id ?? "").trim();
    if (!id) throw new Error(`${collection}[${index}] omitted id`);
    return id;
  });
  if (new Set(ids).size !== ids.length) {
    throw new Error(`${collection} canonical response contains duplicate ids`);
  }
  return ids.sort();
}

function uiStackIDs(body: unknown) {
  const root = requireRecord(body, "stacks UI response");
  if (!Array.isArray(root.data)) {
    throw new Error("stacks UI response omitted data array");
  }
  return uniqueUIIDs(root.data, "stacks");
}

function uiRegistryServiceIDs(body: unknown) {
  const root = requireRecord(body, "services UI response");
  const data = requireRecord(root.data, "services UI data envelope");
  for (const field of ["catalog", "stacks", "servers", "services"]) {
    if (!Array.isArray(data[field])) {
      throw new Error(`services UI response omitted ${field} array`);
    }
  }
  return uniqueUIIDs(data.services, "registry services");
}

function uniqueUIIDs(items: unknown[], label: string) {
  const ids = items.map((item, index) => {
    const record = requireRecord(item, `${label}[${index}]`);
    const id = String(record.id ?? record.name ?? "").trim();
    if (!id) throw new Error(`${label}[${index}] omitted id/name`);
    return id;
  });
  if (new Set(ids).size !== ids.length) {
    throw new Error(`${label} response contains duplicate ids`);
  }
  return ids.sort();
}

function requireRecord(value: unknown, label: string): Record<string, any> {
  if (!isRecord(value)) throw new Error(`${label} must be an object`);
  return value;
}

function isRecord(value: unknown): value is Record<string, any> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

// The shared cloud test identity is confined to a test tenant
// (D-2026-10-22-02): its session is bound to one, and naming another tenant
// never widens what it reads. Read-only, so it is safe on production.
async function verifyTestTenantBoundary(token: string, body: unknown) {
  const whoami = requireRecord(body, "cloud whoami");
  const tenantID = String(whoami.tenantId ?? "").trim();
  const testSubjects = [
    process.env.TEST_RIL_PRO_USER_ID,
    process.env.TEST_ALL_YOU_NEED_USER_ID,
    ...String(process.env.TECHSTACK_TEST_SUBJECTS ?? "").split(","),
  ]
    .map((value) => String(value ?? "").trim())
    .filter(Boolean);
  const demoTenant = String(process.env.TECHSTACK_DEMO_TENANT_ID ?? "").trim();
  const testTenants = new Set([
    ...testSubjects.flatMap((subject) => [`usr:${subject}`, subject]),
    ...(demoTenant ? [demoTenant] : []),
  ]);
  if (testTenants.size > 0) {
    expect(
      tenantID.startsWith("test:") || testTenants.has(tenantID),
      `cloud test identity is bound to a non-test tenant`,
    ).toBe(true);
  } else {
    test.info().annotations.push({
      type: "test-tenant-boundary",
      description:
        "no test subject or demo tenant configured; tenant binding not asserted",
    });
  }

  const own = await authenticatedJSON(token, "/api/v1/stacks");
  expect(own.status).toBe(200);
  const foreign = await authenticatedJSON(token, "/api/v1/stacks", {
    headers: {
      "X-Org-ID": "org_test_boundary_probe",
      "X-Kombify-Tenant-ID": "org_test_boundary_probe",
    },
  });
  if (foreign.status !== 403 && foreign.status !== 401) {
    expect(foreign.status).toBe(200);
    expect(stackIDs(foreign.body)).toEqual(stackIDs(own.body));
  }
}

function stackIDs(body: unknown): string[] {
  const record = (body ?? {}) as Record<string, unknown>;
  const data = record.data ?? record.items ?? body;
  const items = Array.isArray(data)
    ? data
    : Array.isArray((data as Record<string, unknown> | null)?.items)
      ? ((data as Record<string, unknown>).items as unknown[])
      : [];
  return items
    .map((item) =>
      String((item as Record<string, unknown> | null)?.id ?? "").trim(),
    )
    .filter(Boolean)
    .sort();
}

function assertCanonicalDemoPrincipal(body: unknown) {
  const whoami = requireRecord(body, "cloud whoami");
  const expectedTenant = String(
    process.env.TECHSTACK_DEMO_TENANT_ID ?? "",
  ).trim();
  const expectedSubject =
    String(process.env.TECHSTACK_DEMO_USER_IDS ?? "")
      .split(",")
      .map((value) => value.trim())
      .find(Boolean) ?? "";
  if (!expectedTenant || !expectedSubject) {
    throw new Error(
      "Exact inventory profile requires TECHSTACK_DEMO_TENANT_ID and TECHSTACK_DEMO_USER_IDS",
    );
  }
  expect(String(whoami.subject ?? "").trim()).toBe(expectedSubject);
  expect(String(whoami.tenantId ?? "").trim()).toBe(expectedTenant);
}

function isUIAPIURL(url: string, suffix: string) {
  const pathname = new URL(url).pathname.replace(/\/+$/, "");
  return (
    pathname.endsWith(`/api/v1/${suffix}`) ||
    pathname.endsWith(`/v1/techstack/${suffix}`)
  );
}

function assertCandidateUIResponseOrigin(responseURL: string) {
  const product = new URL(PRODUCT_BASE);
  if (!product.hostname.endsWith(".onrender.com")) return;
  expect(new URL(responseURL).origin).toBe(product.origin);
}

function isStackOperationsURL(url: string) {
  const pathname = new URL(url).pathname.replace(/\/+$/, "");
  return /\/(?:api\/v1|v1\/techstack)\/stacks\/[^/]+\/operations$/.test(
    pathname,
  );
}

function isHTTP2xx(value: number) {
  return value >= 200 && value < 300;
}

async function domInventoryIDs(
  page: Parameters<typeof authenticateRuntimeUser>[1],
  testID: string,
  attribute: string,
) {
  const ids = await page
    .getByTestId(testID)
    .evaluateAll(
      (elements, attributeName) =>
        elements.map((element) => element.getAttribute(attributeName) ?? ""),
      attribute,
    );
  if (ids.some((id) => !id.trim())) {
    throw new Error(`${testID} omitted ${attribute}`);
  }
  if (new Set(ids).size !== ids.length) {
    throw new Error(`${testID} contains duplicate ${attribute} values`);
  }
  return ids.sort();
}

function idsSha256(ids: string[]) {
  return createHash("sha256").update(ids.join("\n"), "utf8").digest("hex");
}

function sha256(value: Buffer) {
  return createHash("sha256").update(value).digest("hex");
}

function escapeRegExp(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function firstProductURL(...values: Array<string | undefined>) {
  for (const value of values) {
    const normalized = normalizeURL(value);
    if (normalized && isLiveHTTPSURL(normalized)) return normalized;
  }
  return "";
}

function normalizeURL(value: string | undefined) {
  const raw = value?.trim();
  if (!raw) return "";
  try {
    const parsed = new URL(raw);
    parsed.hash = "";
    parsed.search = "";
    return parsed.toString().replace(/\/+$/g, "");
  } catch {
    return "";
  }
}

function requireLiveProductURL(label: string, value: string) {
  const normalized = normalizeURL(value);
  if (!normalized) {
    throw new Error(`${label} URL is required for SaaS Runtime Auth smoke.`);
  }
  const parsed = new URL(normalized);
  if (parsed.protocol !== "https:" || isLoopbackURL(normalized)) {
    throw new Error(
      `${label} URL must be a real HTTPS product origin, got ${normalized}. Runtime Auth smoke does not run against localhost/self-hosted targets.`,
    );
  }
  return normalized;
}

function isLiveHTTPSURL(value: string) {
  const parsed = new URL(value);
  return parsed.protocol === "https:" && !isLoopbackURL(value);
}

function isLoopbackURL(value: string) {
  const host = new URL(value).hostname.toLowerCase();
  return host === "localhost" || host === "127.0.0.1" || host === "::1";
}

function runtimeAuthRolesFromEnv(value: string | undefined) {
  const requested = value
    ?.split(",")
    .map((item) => item.trim())
    .filter(Boolean);
  if (!requested?.length) return allRoles;

  const roles: RuntimeAuthRole[] = [];
  const invalid: string[] = [];
  for (const role of requested) {
    if (allRoles.includes(role as RuntimeAuthRole)) {
      const typedRole = role as RuntimeAuthRole;
      if (!roles.includes(typedRole)) roles.push(typedRole);
      continue;
    }
    invalid.push(role);
  }
  if (invalid.length > 0) {
    throw new Error(
      `TECHSTACK_RUNTIME_E2E_AUTH_ROLES contains unsupported roles: ${invalid.join(", ")}. Expected one or more of ${allRoles.join(", ")}.`,
    );
  }
  return roles;
}
