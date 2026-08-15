import { type Page } from "@playwright/test";

import {
  requireTechStackTestUser,
  type TechStackTestUserRole,
} from "../../src/lib/testing/techstack-test-users";
import {
  AUTH0_FORM_ERROR_PATTERN,
  compactAuth0Error,
} from "../../src/lib/testing/auth0-errors";

export type RuntimeAuthRole = Extract<
  TechStackTestUserRole,
  "oneliner" | "remote" | "cloud"
>;

export interface RuntimeAuthSession {
  token: string;
}

// RuntimeGatewaySession is intentionally bearer-only. Callers must never log,
// attach, or serialize its token; it exists only to drive a single Gateway
// request inside a short-lived E2E browser context.
export interface RuntimeGatewaySession {
  token: string;
  apiBase: string;
}

export interface RuntimeAuthOptions {
  productBase: string;
  apiBase: string;
  sessionCookieName?: string;
  entryPath?: string;
  expectedAuthIssuer?: string;
}

export async function authenticateRuntimeUser(
  role: RuntimeAuthRole,
  page: Page,
  options: RuntimeAuthOptions,
): Promise<RuntimeAuthSession> {
  const user = requireTechStackTestUser(role);

  await page.goto(options.entryPath ?? "/login", {
    waitUntil: "domcontentloaded",
  });
  const cloudLoginButton = page
    .getByRole("button", {
      name: /Sign in with|Continue with|Create owner with/i,
    })
    .first();
  if (await cloudLoginButton.isVisible({ timeout: 5_000 }).catch(() => false)) {
    await cloudLoginButton.click();
  }
  await fillAuth0UniversalLogin(
    page,
    user.email,
    user.password,
    options.expectedAuthIssuer ??
      process.env.TECHSTACK_AUTH_CLOUD_ISSUER ??
      "https://login.kombify.io",
  );
  await continuePastAuth0PostLoginPrompts(page);
  await waitForRuntimeStacks(page);
  const token = await browserSessionToken(page, options);
  if (!token) {
    throw new Error(
      "Auth0 browser login did not produce a TechStack API session token",
    );
  }
  return { token };
}

export async function fetchBrowserWhoAmI(page: Page): Promise<{
  status: number;
  body: unknown;
}> {
  return await page.evaluate(async () => {
    const response = await fetch("/api/v2/whoami", {
      credentials: "include",
    });
    const text = await response.text();
    let body: unknown = text;
    try {
      body = text ? JSON.parse(text) : null;
    } catch {
      body = text;
    }
    return { status: response.status, body };
  });
}

// captureGatewayApiSession observes the SPA's first-party Gateway request, so
// its bearer is minted for the exact public /v1/techstack route rather than a
// private TechStack session. It never writes the bearer to a log or artifact.
export async function captureGatewayApiSession(
  page: Page,
): Promise<RuntimeGatewaySession> {
  const requestPromise = page.waitForRequest(
    (request) => {
      const url = new URL(request.url());
      return (
        request.method() === "GET" && url.pathname === "/v1/techstack/stacks"
      );
    },
    { timeout: 30_000 },
  );
  await page.goto(`/stacks?gateway_probe=${Date.now()}`, {
    waitUntil: "domcontentloaded",
  });
  const request = await requestPromise;
  const authorization = request.headers()["authorization"] ?? "";
  const token = authorization.replace(/^Bearer\s+/i, "").trim();
  if (!token) {
    throw new Error("Techstack stacks page omitted its Auth0 Gateway token");
  }
  const url = new URL(request.url());
  return { token, apiBase: `${url.origin}/v1/techstack` };
}

async function browserSessionToken(
  page: Page,
  options: RuntimeAuthOptions,
): Promise<string> {
  const localStorageSession = await page.evaluate(() => {
    const raw = window.localStorage.getItem("pocketbase_auth");
    if (!raw) return null;
    const parsed = JSON.parse(raw) as {
      token?: string;
    };
    return { token: parsed.token ?? "" };
  });
  if (localStorageSession?.token) {
    return localStorageSession.token;
  }

  const cookies = await page
    .context()
    .cookies([options.productBase, options.apiBase]);
  return (
    cookies.find(
      (cookie) =>
        cookie.name === (options.sessionCookieName ?? "techstack_session"),
    )?.value ?? ""
  );
}

async function fillAuth0UniversalLogin(
  page: Page,
  email: string,
  password: string,
  expectedIssuer: string,
) {
  const expectedOrigin = requireExpectedIssuerOrigin(expectedIssuer);
  await page.waitForURL((url) => url.origin.toLowerCase() === expectedOrigin, {
    timeout: 60_000,
    waitUntil: "domcontentloaded",
  });
  assertExpectedIssuer(page, expectedOrigin);
  await page.waitForLoadState("domcontentloaded");
  const emailInput = visibleLocator(
    page,
    'input[type="email"], input[name="email"], input[name="username"], #username',
  );
  await waitForAuth0Field(page, emailInput, "email");
  await emailInput.first().fill(email, { timeout: 5_000 });
  const passwordInput = visibleLocator(
    page,
    'input[type="password"]:not([aria-hidden="true"]):not(.hide), input[name="password"]:not([aria-hidden="true"]):not(.hide), #password:not([aria-hidden="true"]):not(.hide)',
  );
  if (
    !(await passwordInput
      .first()
      .isVisible()
      .catch(() => false))
  ) {
    // Auth0's branded identifier action can consume both pointer and keyboard
    // events without posting in headless Linux. Submit the real primary form;
    // this preserves its state, PKCE and connection fields.
    const identifierForm = emailInput
      .first()
      .locator('xpath=ancestor::form[@data-form-primary="true"][1]');
    await identifierForm.evaluate((form: HTMLFormElement) => {
      HTMLFormElement.prototype.submit.call(form);
    });
  }
  await waitForAuth0Field(page, passwordInput, "password");
  assertExpectedIssuer(page, expectedOrigin);
  await passwordInput.first().fill(password, { timeout: 5_000 });
  await page
    .getByRole("button", { name: /^(log in|sign in|continue)$/i })
    .last()
    .click();
}

function requireExpectedIssuerOrigin(value: string) {
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error("Expected Auth0 issuer must be a valid HTTPS URL");
  }
  if (parsed.protocol !== "https:") {
    throw new Error("Expected Auth0 issuer must use HTTPS");
  }
  return parsed.origin.toLowerCase();
}

function assertExpectedIssuer(page: Page, expectedOrigin: string) {
  let actualOrigin = "";
  try {
    actualOrigin = new URL(page.url()).origin.toLowerCase();
  } catch {
    // Preserve the fail-closed mismatch below without including credentials.
  }
  if (actualOrigin !== expectedOrigin) {
    throw new Error(
      `Refusing to enter Auth0 credentials on unexpected issuer origin ${actualOrigin || "invalid"}`,
    );
  }
}

function visibleLocator(page: Page, selector: string) {
  return page.locator(
    selector
      .split(",")
      .map((part) => `${part.trim()}:visible`)
      .join(", "),
  );
}

async function continuePastAuth0PostLoginPrompts(page: Page) {
  for (let attempt = 0; attempt < 3; attempt += 1) {
    await page.waitForLoadState("domcontentloaded").catch(() => undefined);
    const passkeySkip = page.getByText(/continue without passkeys/i).first();
    const visible = await passkeySkip
      .isVisible({ timeout: 8_000 })
      .catch(() => false);
    if (!visible) return;

    const dontShowAgain = page.getByLabel(/don'?t show me this again/i).first();
    if (await dontShowAgain.isVisible({ timeout: 1_000 }).catch(() => false)) {
      await dontShowAgain
        .check({ force: true, timeout: 5_000 })
        .catch(() => undefined);
    }
    await passkeySkip
      .click({ force: true, timeout: 10_000 })
      .catch(async (err) => {
        const clicked = await clickAuth0Text(
          page,
          /continue without passkeys/i,
        );
        if (!clicked) throw err;
      });
  }
}

async function clickAuth0Text(page: Page, pattern: RegExp) {
  return await page.evaluate((source) => {
    const regex = new RegExp(source, "i");
    const elements = Array.from(
      document.querySelectorAll("button, a, [role='button'], input"),
    );
    const target = elements.find((element) =>
      regex.test(
        [
          element.textContent ?? "",
          element.getAttribute("aria-label") ?? "",
          element.getAttribute("value") ?? "",
        ].join(" "),
      ),
    );
    if (!(target instanceof HTMLElement)) return false;
    target.click();
    return true;
  }, pattern.source);
}

async function waitForRuntimeStacks(page: Page) {
  const deadline = Date.now() + 120_000;
  let lastWaitError = "";
  while (Date.now() < deadline) {
    const formError = await auth0FormErrorMessage(page, 500);
    if (formError) {
      throw new Error(
        `Runtime Auth0 login failed before TechStack callback: ${formError}. Current location: ${safeLocation(page.url())}`,
      );
    }
    if (/\/stacks(?:[/?#]|$)/.test(page.url())) return;

    try {
      await page.waitForURL(/\/stacks/, {
        timeout: Math.max(1, Math.min(5_000, deadline - Date.now())),
      });
      return;
    } catch (err) {
      lastWaitError =
        err instanceof Error ? err.name : "navigation wait failed";
    }
  }
  throw new Error(
    `Runtime Auth0 login did not reach /stacks. Current location: ${safeLocation(page.url())}. ${lastWaitError}`,
  );
}

async function auth0FormErrorMessage(page: Page, timeoutMs = 1_000) {
  const visibleTextError = page.getByText(AUTH0_FORM_ERROR_PATTERN).first();
  if (
    await visibleTextError.isVisible({ timeout: timeoutMs }).catch(() => false)
  ) {
    return compactAuth0Error(
      await visibleTextError.innerText({ timeout: 1_000 }).catch(() => ""),
    );
  }

  const alertError = page
    .locator('[role="alert"], [aria-live="assertive"], [aria-live="polite"]')
    .filter({ hasText: AUTH0_FORM_ERROR_PATTERN })
    .first();
  if (await alertError.isVisible({ timeout: 250 }).catch(() => false)) {
    return compactAuth0Error(
      await alertError.innerText({ timeout: 1_000 }).catch(() => ""),
    );
  }

  return "";
}

async function waitForAuth0Field(
  page: Page,
  locator: ReturnType<typeof visibleLocator>,
  label: string,
) {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    const formError = await auth0FormErrorMessage(page, 250);
    if (formError) {
      throw new Error(
        `Runtime Auth0 login stopped before the ${label} field: ${formError}. Current location: ${safeLocation(page.url())}`,
      );
    }
    if (
      await locator
        .first()
        .isVisible()
        .catch(() => false)
    )
      return;
    await page.waitForTimeout(250);
  }
  throw new Error(
    `Runtime Auth0 login did not render the ${label} field. Current location: ${safeLocation(page.url())}`,
  );
}

function safeLocation(value: string) {
  try {
    const parsed = new URL(value);
    return `${parsed.origin}${parsed.pathname}`;
  } catch {
    return "invalid";
  }
}
