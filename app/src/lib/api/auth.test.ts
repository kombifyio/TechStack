import { beforeEach, describe, expect, it, vi } from "vitest";

function requestPath(input: unknown): string {
  const raw = input instanceof Request ? input.url : String(input);
  return new URL(raw, "https://techstack.test").pathname;
}

describe("local session auth API", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.restoreAllMocks();
    vi.stubGlobal("window", {
      location: {
        origin: "https://techstack.test",
      },
    });
  });

  it("uses the shared API client so browser logins include CSRF", async () => {
    const fetchMock = vi.fn();
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ token: "csrf-token-123" }), {
        status: 200,
        headers: {
          "content-type": "application/json",
          "x-csrf-token": "csrf-token-123",
        },
      }),
    );
    fetchMock.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          ok: true,
          email: "admin@techstack.local",
          provider: "pocketbase",
        }),
        {
          status: 200,
          headers: { "content-type": "application/json" },
        },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { loginWithLocalSession } = await import("./auth");
    const result = await loginWithLocalSession(
      "admin@techstack.local",
      "dev-admin-password-change-me",
    );

    expect(result.ok).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(requestPath(fetchMock.mock.calls[0][0])).toBe("/api/v1/csrf");
    expect(requestPath(fetchMock.mock.calls[1][0])).toBe("/api/v1/auth/login");
    const loginHeaders = new Headers(fetchMock.mock.calls[1][1]?.headers);
    expect(loginHeaders.get("x-csrf-token")).toBe("csrf-token-123");
  });

  it("uses the shared API client so portal SSO verification includes CSRF", async () => {
    const fetchMock = vi.fn();
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ token: "csrf-token-456" }), {
        status: 200,
        headers: {
          "content-type": "application/json",
          "x-csrf-token": "csrf-token-456",
        },
      }),
    );
    fetchMock.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            pb_token: "pb-token",
            user: { id: "user-1", email: "user@example.com", name: "User" },
            cloud_user: {
              sub: "auth0|user",
              email: "user@example.com",
              name: "User",
              is_admin: true,
            },
          },
        }),
        {
          status: 200,
          headers: { "content-type": "application/json" },
        },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { verifyPortalToken } = await import("./auth");
    const result = await verifyPortalToken("portal-token");

    expect(result.pb_token).toBe("pb-token");
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(requestPath(fetchMock.mock.calls[0][0])).toBe("/api/v1/csrf");
    expect(requestPath(fetchMock.mock.calls[1][0])).toBe(
      "/api/v1/auth/portal-verify",
    );
    const verifyHeaders = new Headers(fetchMock.mock.calls[1][1]?.headers);
    expect(verifyHeaders.get("x-csrf-token")).toBe("csrf-token-456");
  });

  it("accepts portal session confirmation only from a 200 V2 whoami response", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          subject: "auth0|user",
          tenantId: "default",
        }),
        { status: 202, headers: { "content-type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { getV2WhoAmI } = await import("./auth");

    await expect(getV2WhoAmI()).rejects.toThrow("V2 whoami failed: 202");
    expect(fetchMock).toHaveBeenCalledWith("/api/v2/whoami", {
      credentials: "include",
    });
  });
});
