import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  ApiRequestError,
  parseApiErrorResponse,
  resolveGatewayUrl,
} from "./client";

function requestPath(input: unknown): string {
  const raw = input instanceof Request ? input.url : String(input);
  return new URL(raw, "https://techstack.test").pathname;
}

function browserWindow() {
  const win = {
    location: {
      origin: "https://techstack.test",
      search: "",
    },
    localStorage: {
      getItem: vi.fn(() => null),
      setItem: vi.fn(),
      removeItem: vi.fn(),
    },
  } as any;
  win.parent = win;
  return win;
}

function browserIframeWindow() {
  const win = browserWindow();
  win.parent = { postMessage: vi.fn() };
  return win;
}

afterEach(() => {
  vi.unstubAllEnvs();
  vi.doUnmock("#lib/auth/gateway-auth.js");
  vi.doUnmock("#lib/stores/postMessageBridge.js");
  vi.doUnmock("#lib/api/session-reprojection.js");
  vi.doUnmock("$app/env");
});

describe("parseApiErrorResponse", () => {
  it("prefers the standardized { error: { message } } envelope", async () => {
    const response = new Response(
      JSON.stringify({ error: { code: "BAD_REQUEST", message: "Nope" } }),
      {
        status: 400,
        headers: {
          "content-type": "application/json",
          "x-request-id": "req-123",
        },
      },
    );

    const err = await parseApiErrorResponse(response, {
      action: "Test action",
      method: "POST",
    });

    expect(err).toBeInstanceOf(ApiRequestError);
    expect(err.message).toContain("Nope");
    expect(err.status).toBe(400);
    expect(err.code).toBe("BAD_REQUEST");
    expect(err.requestId).toBe("req-123");
    expect(err.details).toBeTruthy();
  });

  it("falls back to PocketBase-style { message, data } shape", async () => {
    const response = new Response(
      JSON.stringify({
        code: 400,
        message: "Failed to create record.",
        data: {
          name: { code: "validation_required", message: "Cannot be blank." },
        },
      }),
      {
        status: 400,
        headers: { "content-type": "application/json" },
      },
    );

    const err = await parseApiErrorResponse(response);
    expect(err.message).toContain("Failed to create record");
    expect(err.status).toBe(400);
    expect(err.details).toBeTruthy();
  });

  it("produces a helpful message for HTML error bodies", async () => {
    const response = new Response("<html><body>oops</body></html>", {
      status: 502,
      headers: { "content-type": "text/html" },
    });

    const err = await parseApiErrorResponse(response, { action: "Request" });
    expect(err.status).toBe(502);
    expect(err.message).toContain("Server returned HTML instead of JSON");
    expect(err.code).toBe("upstream_unavailable");
    expect(err.retryable).toBe(true);
  });
});

describe("fetchApi CSRF handling", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.restoreAllMocks();
    vi.stubGlobal("window", browserWindow());
  });

  it("adds a CSRF token for unsafe browser cookie-auth requests", async () => {
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
      new Response(JSON.stringify({ data: { ok: true } }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { fetchApi } = await import("./client");
    await fetchApi<{ ok: boolean }>("/api/v1/stacks", {
      method: "POST",
      body: JSON.stringify({ name: "demo" }),
    });

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(requestPath(fetchMock.mock.calls[0][0])).toBe("/api/v1/csrf");
    const mutationHeaders = new Headers(fetchMock.mock.calls[1][1]?.headers);
    expect(mutationHeaders.get("x-csrf-token")).toBe("csrf-token-123");
  });
});

describe("fetchApi rate-limit recovery", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.restoreAllMocks();
    vi.stubGlobal("window", browserWindow());
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("honors the full server retry floor before replaying one read", async () => {
    vi.useFakeTimers();
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ error: { code: "rate_limited" } }), {
          status: 429,
          headers: {
            "content-type": "application/json",
            "retry-after": "60",
          },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ data: { ok: true } }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { fetchApi } = await import("./client");
    const resultPromise = fetchApi<{ ok: boolean }>("/api/v1/features");

    await vi.advanceTimersByTimeAsync(59_999);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    const result = await resultPromise;

    expect(result.data.ok).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("pauses additional reads to the same origin during the retry window", async () => {
    vi.useFakeTimers();
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ error: { code: "rate_limited" } }), {
          status: 429,
          headers: {
            "content-type": "application/json",
            "retry-after": "60",
          },
        }),
      )
      .mockImplementation(() =>
        Promise.resolve(
          new Response(JSON.stringify({ data: { ok: true } }), {
            status: 200,
            headers: { "content-type": "application/json" },
          }),
        ),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { fetchApi } = await import("./client");
    const recoveringRead = fetchApi<{ ok: boolean }>("/api/v1/features");
    await vi.advanceTimersByTimeAsync(0);
    const laterRead = fetchApi<{ ok: boolean }>("/api/v1/stacks");

    await vi.advanceTimersByTimeAsync(59_999);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    await Promise.all([recoveringRead, laterRead]);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("never automatically replays a rate-limited mutation", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: { code: "rate_limited" } }), {
        status: 429,
        headers: { "content-type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { fetchApi } = await import("./client");
    await expect(
      fetchApi("/api/v1/stacks", {
        method: "POST",
        headers: { Authorization: "Bearer test" },
      }),
    ).rejects.toMatchObject({ status: 429 });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe("fetchApi embedded gateway auth", () => {
  const gatewayBase = "https://api.kombify.io/v1/techstack";

  beforeEach(() => {
    vi.resetModules();
    vi.restoreAllMocks();
    const win = browserIframeWindow();
    vi.stubGlobal("window", win);
    vi.stubGlobal("localStorage", win.localStorage);
    vi.doMock("$app/env", () => ({ browser: true }));
  });

  it("uses a parent gateway token for embedded requests when silent Auth0 fails", async () => {
    vi.stubEnv("VITE_GATEWAY_API_BASE", gatewayBase);
    const getGatewayToken = vi
      .fn()
      .mockRejectedValue(new Error("silent auth blocked"));
    const initBridge = vi.fn();
    const requestGatewayToken = vi
      .fn()
      .mockResolvedValue("parent-gateway-token");
    const requestAuthToken = vi.fn().mockResolvedValue("sso-token");
    vi.doMock("#lib/auth/gateway-auth.js", () => ({
      getGatewayToken,
    }));
    vi.doMock("#lib/stores/postMessageBridge.js", () => ({
      initBridge,
      requestGatewayToken,
      requestAuthToken,
    }));

    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ data: { ok: true } }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { deploymentMode } = await import("#lib/stores/deploymentMode.js");
    deploymentMode.setMode("saas-embedded");
    const { fetchApi } = await import("./client");

    await fetchApi<{ ok: boolean }>("/api/v1/features");

    expect(getGatewayToken).toHaveBeenCalled();
    expect(initBridge).toHaveBeenCalled();
    expect(requestGatewayToken).toHaveBeenCalled();
    expect(requestAuthToken).not.toHaveBeenCalled();
    expect(String(fetchMock.mock.calls[0][0])).toBe(
      "https://api.kombify.io/v1/techstack/features",
    );
    const headers = new Headers(fetchMock.mock.calls[0][1]?.headers);
    expect(headers.get("authorization")).toBe("Bearer parent-gateway-token");
  });

  it("fails closed instead of falling back to same-origin when gateway auth is unavailable", async () => {
    vi.stubEnv("VITE_GATEWAY_API_BASE", gatewayBase);
    vi.doMock("#lib/auth/gateway-auth.js", () => ({
      getGatewayToken: vi.fn().mockRejectedValue(new Error("login_required")),
    }));
    vi.doMock("#lib/stores/postMessageBridge.js", () => ({
      initBridge: vi.fn(),
      requestGatewayToken: vi.fn().mockRejectedValue(new Error("no parent")),
      requestAuthToken: vi.fn().mockResolvedValue("sso-token"),
    }));
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    const { deploymentMode } = await import("#lib/stores/deploymentMode.js");
    deploymentMode.setMode("saas-embedded");
    const { fetchApi } = await import("./client");

    await expect(fetchApi("/api/v1/features")).rejects.toMatchObject({
      code: "gateway_auth_unavailable",
      status: 401,
    });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("routes standalone SaaS feature checks through the gateway", async () => {
    const win = browserWindow();
    vi.stubGlobal("window", win);
    vi.stubGlobal("localStorage", win.localStorage);
    vi.stubEnv("VITE_GATEWAY_API_BASE", gatewayBase);
    vi.doMock("#lib/auth/gateway-auth.js", () => ({
      getGatewayToken: vi.fn().mockResolvedValue("auth0-api-token"),
    }));
    vi.doMock("#lib/stores/postMessageBridge.js", () => ({
      initBridge: vi.fn(),
      requestGatewayToken: vi.fn(),
      requestAuthToken: vi.fn(),
    }));
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ data: { ok: true } }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { deploymentMode } = await import("#lib/stores/deploymentMode.js");
    await deploymentMode.init("saas");
    const { fetchApi } = await import("./client");

    await fetchApi<{ ok: boolean }>("/api/v1/features");

    expect(String(fetchMock.mock.calls[0][0])).toBe(
      "https://api.kombify.io/v1/techstack/features",
    );
    const headers = new Headers(fetchMock.mock.calls[0][1]?.headers);
    expect(headers.get("authorization")).toBe("Bearer auth0-api-token");
  });

});

describe("fetchApi session-reprojection interceptor", () => {
  const reprojectionResponse = () =>
    new Response(
      JSON.stringify({
        error: {
          code: "UNAUTHORIZED",
          message: "Session requires re-authentication",
          details: {
            reason_code: "session_reprojection_required",
            retryable: true,
          },
        },
      }),
      { status: 401, headers: { "content-type": "application/json" } },
    );

  beforeEach(() => {
    vi.resetModules();
    vi.restoreAllMocks();
    vi.stubGlobal("window", browserWindow());
  });

  it("replays the request exactly once after an in-place recovery", async () => {
    const recoverFromSessionReprojection = vi.fn().mockResolvedValue("retry");
    vi.doMock("#lib/api/session-reprojection.js", async (importOriginal) => ({
      ...(await importOriginal<
        typeof import("#lib/api/session-reprojection.js")
      >()),
      recoverFromSessionReprojection,
    }));

    const fetchMock = vi.fn();
    fetchMock.mockResolvedValueOnce(reprojectionResponse());
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ data: { ok: true } }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { fetchApi } = await import("./client");
    const result = await fetchApi<{ ok: boolean }>("/api/v1/features");

    expect(result.data.ok).toBe(true);
    expect(recoverFromSessionReprojection).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("never replays more than once even when the replay 401s again", async () => {
    const recoverFromSessionReprojection = vi.fn().mockResolvedValue("retry");
    vi.doMock("#lib/api/session-reprojection.js", async (importOriginal) => ({
      ...(await importOriginal<
        typeof import("#lib/api/session-reprojection.js")
      >()),
      recoverFromSessionReprojection,
    }));

    const fetchMock = vi.fn().mockResolvedValue(reprojectionResponse());
    vi.stubGlobal("fetch", fetchMock);

    const { fetchApi } = await import("./client");
    await expect(fetchApi("/api/v1/features")).rejects.toMatchObject({
      status: 401,
    });
    // One recovery attempt, one replay - the replayed 401 is thrown as-is.
    expect(recoverFromSessionReprojection).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("throws the original error when recovery requires re-auth", async () => {
    const recoverFromSessionReprojection = vi
      .fn()
      .mockResolvedValue("reauth_required");
    vi.doMock("#lib/api/session-reprojection.js", async (importOriginal) => ({
      ...(await importOriginal<
        typeof import("#lib/api/session-reprojection.js")
      >()),
      recoverFromSessionReprojection,
    }));

    const fetchMock = vi.fn().mockResolvedValue(reprojectionResponse());
    vi.stubGlobal("fetch", fetchMock);

    const { fetchApi } = await import("./client");
    await expect(fetchApi("/api/v1/features")).rejects.toMatchObject({
      status: 401,
    });
    expect(recoverFromSessionReprojection).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("does not intercept 401s without the reprojection reason code", async () => {
    const recoverFromSessionReprojection = vi.fn();
    vi.doMock("#lib/api/session-reprojection.js", async (importOriginal) => ({
      ...(await importOriginal<
        typeof import("#lib/api/session-reprojection.js")
      >()),
      recoverFromSessionReprojection,
    }));

    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          error: { code: "UNAUTHORIZED", message: "Authentication required" },
        }),
        { status: 401, headers: { "content-type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { fetchApi } = await import("./client");
    await expect(fetchApi("/api/v1/features")).rejects.toMatchObject({
      status: 401,
    });
    expect(recoverFromSessionReprojection).not.toHaveBeenCalled();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe("resolveGatewayUrl", () => {
  const base = "https://api.kombify.io/v1/techstack";

  it("maps /api/v1/* to the gateway base in embedded saas mode", () => {
    expect(
      resolveGatewayUrl("/api/v1/features", {
        mode: "saas-embedded",
        gatewayBase: base,
      }),
    ).toBe("https://api.kombify.io/v1/techstack/features");
    expect(
      resolveGatewayUrl("/api/v1/stacks?page=2", {
        mode: "saas-embedded",
        gatewayBase: base,
      }),
    ).toBe("https://api.kombify.io/v1/techstack/stacks?page=2");
  });

  it("maps /api/v1/* to the gateway base in SaaS standalone data-plane mode", () => {
    expect(
      resolveGatewayUrl("/api/v1/features", {
        mode: "self-hosted",
        dataPlane: "gateway",
        gatewayBase: base,
      }),
    ).toBe("https://api.kombify.io/v1/techstack/features");
  });

  it("keeps candidate previews on their same-origin API", () => {
    expect(
      resolveGatewayUrl("/api/v1/inventory/servers", {
        mode: "self-hosted",
        dataPlane: "gateway",
        kombifyEdition: "preview",
        gatewayBase: base,
      }),
    ).toBeNull();
    expect(
      resolveGatewayUrl("/api/v1/inventory/services", {
        mode: "saas-embedded",
        dataPlane: "gateway",
        kombifyEdition: " PREVIEW ",
        gatewayBase: base,
      }),
    ).toBeNull();
  });

  it("maps cloud-edition /api/v1/* to gateway even before deployment mode finishes", () => {
    expect(
      resolveGatewayUrl("/api/v1/stacks", {
        mode: "self-hosted",
        dataPlane: "same-origin",
        kombifyEdition: "cloud",
        gatewayBase: base,
      }),
    ).toBe("https://api.kombify.io/v1/techstack/stacks");
  });

  it("maps saas-standalone mutations to gateway even when the mode store stayed on its same-origin default", () => {
    expect(
      resolveGatewayUrl("/api/v1/stacks/stack-1/provision", {
        mode: "self-hosted",
        dataPlane: "same-origin",
        kombifyEdition: "saas-standalone",
        gatewayBase: base,
        method: "POST",
      }),
    ).toBe("https://api.kombify.io/v1/techstack/stacks/stack-1/provision");
  });

  it("routes the cloud-edition create mutation through the gateway", () => {
    expect(
      resolveGatewayUrl("/api/v1/stacks", {
        mode: "self-hosted",
        dataPlane: "same-origin",
        kombifyEdition: "cloud",
        gatewayBase: base,
        method: "POST",
      }),
    ).toBe("https://api.kombify.io/v1/techstack/stacks");
    expect(
      resolveGatewayUrl("/api/v1/stacks", {
        mode: "self-hosted",
        dataPlane: "same-origin",
        kombifyEdition: "cloud",
        gatewayBase: base,
        method: "GET",
      }),
    ).toBe("https://api.kombify.io/v1/techstack/stacks");
  });

  it("returns null in self-hosted mode", () => {
    expect(
      resolveGatewayUrl("/api/v1/features", {
        mode: "self-hosted",
        gatewayBase: base,
      }),
    ).toBeNull();
  });

  it("returns null when no gateway base is configured", () => {
    expect(
      resolveGatewayUrl("/api/v1/features", {
        mode: "saas-embedded",
        gatewayBase: "",
      }),
    ).toBeNull();
  });

  it("keeps non-/api/v1 endpoints same-origin (auth/bootstrap)", () => {
    expect(
      resolveGatewayUrl("/api/v1/auth/mode", {
        mode: "self-hosted",
        dataPlane: "gateway",
        gatewayBase: base,
      }),
    ).toBeNull();
    expect(
      resolveGatewayUrl("/api/v1/csrf", {
        mode: "self-hosted",
        dataPlane: "gateway",
        gatewayBase: base,
      }),
    ).toBeNull();
    expect(
      resolveGatewayUrl("/api/v2/auth/login", {
        mode: "saas-embedded",
        gatewayBase: base,
      }),
    ).toBeNull();
    expect(
      resolveGatewayUrl(
        "/api/v1/auth/portal-verify".replace("/api/v1", "/auth"),
        {
          mode: "saas-embedded",
          gatewayBase: base,
        },
      ),
    ).toBeNull();
  });

  it("keeps reauthentication-bound server mutations on the session origin", () => {
    expect(
      resolveGatewayUrl("/api/v1/servers/server-1/terminal-sessions", {
        mode: "saas-embedded",
        gatewayBase: base,
        method: "POST",
      }),
    ).toBeNull();
    expect(
      resolveGatewayUrl("/api/v1/servers/server-1/access/authorized-key", {
        mode: "saas-embedded",
        gatewayBase: base,
        method: "POST",
      }),
    ).toBeNull();
  });

  it("ignores absolute URLs", () => {
    expect(
      resolveGatewayUrl("https://other.example.com/api/v1/x", {
        mode: "saas-embedded",
        gatewayBase: base,
      }),
    ).toBeNull();
  });
});
