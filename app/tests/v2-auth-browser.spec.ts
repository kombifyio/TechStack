import { expect, test } from "@playwright/test";

async function mockFirstRunLocalOwnerSetup(
  page: import("@playwright/test").Page,
  onSetupRequest: (body: Record<string, unknown> | null) => void,
) {
  await page.route("**/api/v1/auth/mode", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          mode: "local",
          deployment_mode: "self-hosted",
          is_first_run: true,
          cloud_auth_url: null,
          portal_url: null,
          allow_local_login: true,
        },
      }),
    });
  });

  await page.route("**/api/v2/auth/providers", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ providers: [] }),
    });
  });

  await page.route("**/api/v1/auth/methods", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        providers: [],
        breakglass: {
          initialized: false,
          claimed: false,
          email: "",
          has_pending_reveal: false,
          reveal_expires_at: null,
          locked: false,
        },
      }),
    });
  });

  await page.route("**/api/v1/auth/setup", async (route) => {
    onSetupRequest(
      (route.request().postDataJSON() || null) as Record<
        string,
        unknown
      > | null,
    );
    await route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({
        error: { message: "mocked setup failure" },
      }),
    });
  });
}

test.describe.serial("V2 auth browser flow", () => {
  test("self-hosted login entry page offers owner sign-in and sign-up choices", async ({
    page,
  }) => {
    await page.goto("/login");

    await expect(page.getByRole("tab", { name: "Sign In" })).toBeVisible();
    await expect(page.getByRole("tab", { name: "Sign Up" })).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Sign in with kombify Cloud" }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Local owner account" }),
    ).toBeVisible();

    await page.getByRole("tab", { name: "Sign Up" }).click();
    await expect(
      page.getByRole("button", { name: "Create owner with kombify Cloud" }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Create local owner account" }),
    ).toBeVisible();
    await expect(page.getByText("Separate recovery path")).toBeVisible();
  });

  test("SaaS login entry page only exposes kombify Cloud SSO", async ({
    page,
  }) => {
    await page.route("**/api/v1/auth/mode", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            mode: "cloud",
            deployment_mode: "saas",
            is_first_run: false,
            cloud_auth_url: "/api/v2/auth/login?provider=primary",
            portal_url: "https://kombify.io",
            allow_local_login: false,
          },
        }),
      });
    });
    await page.route("**/api/v2/auth/providers", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          providers: [
            {
              id: "primary",
              kind: "auth0",
              label: "kombify Cloud",
              issuer: "https://login.kombify.io/",
            },
          ],
        }),
      });
    });
    await page.route("**/api/v2/whoami", async (route) => {
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({ error: "missing session token" }),
      });
    });
    await page.route("**/api/v1/auth/methods", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          providers: [
            {
              id: "primary",
              kind: "auth0",
              label: "kombify Cloud",
              auth_url: "/api/v2/auth/login?provider=primary",
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
      });
    });

    await page.goto("/login?manual=1");

    await expect(
      page.getByRole("button", { name: "Continue with kombify Cloud" }),
    ).toBeVisible();
    await expect(page.getByRole("tab", { name: "Sign In" })).toHaveCount(0);
    await expect(page.getByRole("tab", { name: "Sign Up" })).toHaveCount(0);
    await expect(
      page.getByRole("heading", { name: "Emergency admin" }),
    ).toHaveCount(0);
    await expect(page.getByText("SELFHOSTED-AUTH.md")).toHaveCount(0);
  });

  test("cloud owner sign-in starts the V2 auth redirect with a /stacks return target", async ({
    page,
  }) => {
    const loginRequest = page.waitForRequest(
      (request) =>
        request.method() === "GET" &&
        request.url().includes("/api/v2/auth/login"),
    );

    await page.goto("/login");
    await expect(
      page.getByRole("button", { name: "Sign in with kombify Cloud" }),
    ).toBeVisible();

    await page
      .getByRole("button", { name: "Sign in with kombify Cloud" })
      .click();
    const request = await loginRequest;
    const requestedLoginURL = new URL(request.url());

    expect(requestedLoginURL.searchParams.get("return_to")).toBe("/stacks");
    await page.waitForURL(/\/login\?manual=1/);
    await expect(page.getByRole("alert")).toContainText("mocked_v2_login");
  });

  test("callback session errors explain that Auth0 completed but TechStack session creation failed", async ({
    page,
  }) => {
    await page.route("**/api/v1/auth/mode", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            mode: "cloud",
            deployment_mode: "saas",
            is_first_run: false,
            cloud_auth_url: "/api/v2/auth/login",
            portal_url: "https://kombify.io",
            allow_local_login: false,
          },
        }),
      });
    });
    await page.route("**/api/v2/auth/providers", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          providers: [
            {
              id: "primary",
              kind: "auth0",
              label: "kombify Cloud",
              issuer: "https://login.kombify.io/",
            },
          ],
        }),
      });
    });
    await page.route("**/api/v2/whoami", async (route) => {
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({ error: "missing session token" }),
      });
    });
    await page.route("**/api/v1/auth/methods", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
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
      });
    });

    await page.goto("/login?manual=1&error=callback_session_failed");

    await expect(page.getByRole("alert")).toContainText(
      "kombify Cloud sign-in completed, but TechStack could not create a browser session.",
    );
    await expect(page).toHaveURL(/\/login\?manual=1$/);
  });

  test("first-run local owner sign-up uses the auth setup endpoint", async ({
    page,
  }) => {
    let setupBody: Record<string, unknown> | null = null;

    await mockFirstRunLocalOwnerSetup(page, (body) => {
      setupBody = body;
    });

    await page.goto("/login");
    await page.getByRole("tab", { name: "Sign Up" }).click();
    await page.locator("#owner-signup-email").fill("owner@example.com");
    await page.locator("#owner-signup-password").fill("supersecret123");
    await page.locator("#owner-signup-password-confirm").fill("supersecret123");
    await page
      .getByRole("button", { name: "Create local owner account" })
      .click();

    await expect(page.getByText("mocked setup failure")).toBeVisible();
    expect(setupBody).toEqual({
      mode: "local",
      name: "owner",
      email: "owner@example.com",
      password: "supersecret123",
    });
  });

  test("first-run register page uses the auth setup endpoint", async ({
    page,
  }) => {
    let setupBody: Record<string, unknown> | null = null;

    await mockFirstRunLocalOwnerSetup(page, (body) => {
      setupBody = body;
    });

    await page.goto("/register");
    await page.locator("#email").fill("owner@example.com");
    await page.locator("#password").fill("supersecret123");
    await page.locator("#passwordConfirm").fill("supersecret123");
    await page.getByRole("button", { name: "Create Account" }).click();

    await expect(page.getByText("mocked setup failure")).toBeVisible();
    expect(setupBody).toEqual({
      mode: "local",
      name: "owner",
      email: "owner@example.com",
      password: "supersecret123",
    });
  });

  test("auth logout keeps V2 SaaS users on the manual logged-out screen", async ({
    context,
    page,
    baseURL,
  }) => {
    await page.route("**/api/v1/auth/mode", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            mode: "cloud",
            deployment_mode: "saas",
            is_first_run: false,
            cloud_auth_url: "/api/v2/auth/login?provider=primary",
            portal_url: "https://kombify.io",
            allow_local_login: false,
          },
        }),
      });
    });
    await page.route("**/api/v2/auth/providers", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          providers: [
            {
              id: "primary",
              kind: "auth0",
              label: "kombify Cloud",
              issuer: "https://login.kombify.io/",
            },
          ],
        }),
      });
    });
    await page.route("**/api/v2/whoami", async (route) => {
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({ error: "missing session token" }),
      });
    });
    await page.route("**/api/v1/auth/methods", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          providers: [
            {
              id: "primary",
              kind: "auth0",
              label: "kombify Cloud",
              auth_url: "/api/v2/auth/login?provider=primary",
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
      });
    });
    await page.route("**/api/v2/auth/logout?**", async (route) => {
      const requestURL = new URL(route.request().url());
      await route.fulfill({
        status: 302,
        headers: {
          location:
            requestURL.searchParams.get("next") ||
            "/login?manual=1&logged_out=1",
        },
      });
    });

    const logoutRequest = page.waitForRequest(
      (request) =>
        request.method() === "GET" &&
        request.url().includes("/api/v2/auth/logout"),
    );

    if (!baseURL) {
      throw new Error("baseURL is required for the V2 browser auth test");
    }

    await context.addCookies([
      {
        name: "techstack_session",
        value: "session-token",
        url: baseURL,
        httpOnly: true,
        sameSite: "Lax",
      },
    ]);

    await page.goto("/auth/logout");
    const request = await logoutRequest;
    const requestedLogoutURL = new URL(request.url());

    expect(requestedLogoutURL.searchParams.get("next")).toBe(
      "/login?manual=1&logged_out=1",
    );
    await page.goto("/login?manual=1&logged_out=1");
    await expect(page.getByText("You have been signed out.")).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Continue with kombify Cloud" }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Emergency admin" }),
    ).toHaveCount(0);
  });
});
