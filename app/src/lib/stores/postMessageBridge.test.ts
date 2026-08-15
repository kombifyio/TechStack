// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("$app/environment", () => ({
  browser: true,
}));

vi.mock("$app/navigation", () => ({
  goto: vi.fn(),
}));

vi.mock("./theme", () => ({
  theme: { set: vi.fn(), setSystemCardShape: vi.fn() },
}));

vi.mock("./stackIdentity", () => ({
  setStackIdentity: vi.fn(),
}));

class ResizeObserverMock {
  observe = vi.fn();
  disconnect = vi.fn();
}

function setParent(parent: object): void {
  Object.defineProperty(window, "parent", {
    value: parent,
    configurable: true,
  });
  Object.defineProperty(document, "referrer", {
    value: "https://kombify.io/dashboard/tools/stack",
    configurable: true,
  });
}

function portalMessage(parent: object, data: Record<string, unknown>): void {
  window.dispatchEvent(
    new MessageEvent("message", {
      origin: "https://kombify.io",
      source: parent as MessageEventSource,
      data,
    }),
  );
}

describe("postMessageBridge token lanes", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.stubGlobal("ResizeObserver", ResizeObserverMock);
  });

  afterEach(async () => {
    const bridge = await import("./postMessageBridge");
    bridge.destroyBridge();
    vi.unstubAllGlobals();
  });

  it("uses a cached parent SSO token for auth-token requests", async () => {
    const parent = { postMessage: vi.fn() };
    setParent(parent);

    const { initBridge, requestAuthToken } =
      await import("./postMessageBridge");
    initBridge("https://kombify.io");
    portalMessage(parent, { type: "auth-token", token: "sso-token" });
    parent.postMessage.mockClear();

    await expect(requestAuthToken()).resolves.toBe("sso-token");
    expect(parent.postMessage).not.toHaveBeenCalled();
  });

  it("applies theme and card shape through the existing appearance message", async () => {
    const parent = { postMessage: vi.fn() };
    setParent(parent);

    const bridge = await import("./postMessageBridge");
    const { theme } = await import("./theme");
    bridge.initBridge("https://kombify.io");

    portalMessage(parent, {
      type: "theme",
      value: "dark",
      systemCardShape: "app",
    });

    expect(theme.set).toHaveBeenCalledWith("dark");
    expect(theme.setSystemCardShape).toHaveBeenCalledWith("app");
  });

  it("coalesces waiters and re-sends one lost auth request after the rate-limit floor", async () => {
    vi.useFakeTimers();
    const parent = { postMessage: vi.fn() };
    setParent(parent);

    const { initBridge, requestAuthToken } =
      await import("./postMessageBridge");
    initBridge("https://kombify.io");
    parent.postMessage.mockClear();

    const first = requestAuthToken();
    const second = requestAuthToken();
    expect(parent.postMessage).toHaveBeenCalledTimes(1);
    expect(parent.postMessage).toHaveBeenLastCalledWith(
      { type: "auth-request", tool: "kombifystack" },
      "https://kombify.io",
    );

    await vi.advanceTimersByTimeAsync(6_499);
    expect(parent.postMessage).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(1);
    expect(parent.postMessage).toHaveBeenCalledTimes(2);
    expect(parent.postMessage).toHaveBeenLastCalledWith(
      { type: "auth-request", tool: "kombifystack" },
      "https://kombify.io",
    );

    portalMessage(parent, { type: "auth-token", token: "recovered-sso" });
    await expect(Promise.all([first, second])).resolves.toEqual([
      "recovered-sso",
      "recovered-sso",
    ]);

    await vi.advanceTimersByTimeAsync(20_000);
    expect(parent.postMessage).toHaveBeenCalledTimes(2);
    vi.useRealTimers();
  });

  it("requests gateway tokens over a separate message lane", async () => {
    const parent = { postMessage: vi.fn() };
    setParent(parent);

    const { initBridge, requestGatewayToken } =
      await import("./postMessageBridge");
    initBridge("https://kombify.io");
    parent.postMessage.mockClear();

    const tokenPromise = requestGatewayToken("https://api.kombify.io");
    expect(parent.postMessage).toHaveBeenCalledWith(
      {
        type: "gateway-token-request",
        tool: "kombifystack",
        audience: "https://api.kombify.io",
      },
      "https://kombify.io",
    );

    portalMessage(parent, {
      type: "gateway-token",
      token: "auth0-api-token",
      audience: "https://api.kombify.io",
      expiresAt: Date.now() + 60_000,
    });

    await expect(tokenPromise).resolves.toBe("auth0-api-token");
  });

  it("correlates accepted and session-ready AI handover acknowledgements", async () => {
    const parent = { postMessage: vi.fn() };
    setParent(parent);

    const bridge = await import("./postMessageBridge");
    bridge.initBridge("https://kombify.io");
    parent.postMessage.mockClear();

    const request = {
      schema: "kombify.ai-handover.v1" as const,
      targetPanelMode: "support" as const,
      agentId: "kombify-support" as const,
      prompt: "Explain the failed rollout",
      errorContext: {
        schema: "kombify.error-handover.v1",
        source: "kombify-techstack",
        surface: "runtime-rollout",
        title: "Rollout failed",
        userMessage: "The rollout failed.",
        occurredAt: "2026-07-19T12:00:00.000Z",
        optionalValue: undefined,
      },
    };
    const updates: import("./postMessageBridge").AiErrorHandoverUpdate[] = [];

    expect(
      bridge.requestAiErrorHandover(request, (update) => updates.push(update)),
    ).toBe(true);
    expect(parent.postMessage).toHaveBeenCalledTimes(1);
    expect(parent.postMessage.mock.calls[0]?.[0]).not.toHaveProperty(
      "error_context.optionalValue",
    );

    const correlationId = bridge.createAiErrorHandoverCorrelation(request);
    portalMessage(parent, {
      type: "ai-handover-ack",
      tool: "kombifystack",
      schema: "kombify.ai-handover-ack.v1",
      handover_schema: "kombify.ai-handover.v1",
      correlation_id: correlationId,
      status: "accepted",
    });
    portalMessage(parent, {
      type: "ai-handover-ack",
      tool: "kombifystack",
      schema: "kombify.ai-handover-ack.v1",
      handover_schema: "kombify.ai-handover.v1",
      correlation_id: correlationId,
      status: "session-ready",
      session_id: "session-123",
    });

    expect(updates).toEqual([
      { status: "accepted" },
      { status: "session-ready", sessionId: "session-123" },
    ]);
  });

  it("ignores uncorrelated AI acknowledgements and exposes correlated failures", async () => {
    const parent = { postMessage: vi.fn() };
    setParent(parent);

    const bridge = await import("./postMessageBridge");
    bridge.initBridge("https://kombify.io");
    const request = {
      schema: "kombify.ai-handover.v1" as const,
      targetPanelMode: "support" as const,
      agentId: "kombify-support" as const,
      prompt: "Explain the failed rollout",
      errorContext: {
        schema: "kombify.error-handover.v1",
        source: "kombify-techstack",
        surface: "runtime-rollout",
        title: "Rollout failed",
        userMessage: "The rollout failed.",
        occurredAt: "2026-07-19T12:00:00.000Z",
      },
    };
    const updates: import("./postMessageBridge").AiErrorHandoverUpdate[] = [];
    bridge.requestAiErrorHandover(request, (update) => updates.push(update));
    const correlationId = bridge.createAiErrorHandoverCorrelation(request);

    const acknowledgement = {
      type: "ai-handover-ack",
      tool: "kombifystack",
      schema: "kombify.ai-handover-ack.v1",
      handover_schema: "kombify.ai-handover.v1",
      status: "failed",
      error: {
        code: "support_agent_unavailable",
        message: "The requested support agent is unavailable.",
        retryable: false,
      },
    };
    portalMessage(parent, {
      ...acknowledgement,
      correlation_id: "tsh_0000000000000000",
    });
    expect(updates).toEqual([]);

    window.dispatchEvent(
      new MessageEvent("message", {
        origin: "https://attacker.example",
        source: parent as unknown as MessageEventSource,
        data: { ...acknowledgement, correlation_id: correlationId },
      }),
    );
    window.dispatchEvent(
      new MessageEvent("message", {
        origin: "https://kombify.io",
        source: { postMessage: vi.fn() } as unknown as MessageEventSource,
        data: { ...acknowledgement, correlation_id: correlationId },
      }),
    );
    portalMessage(parent, {
      ...acknowledgement,
      schema: "kombify.ai-handover-ack.v0",
      correlation_id: correlationId,
    });
    expect(updates).toEqual([]);

    portalMessage(parent, {
      ...acknowledgement,
      correlation_id: correlationId,
    });
    expect(updates).toEqual([
      {
        status: "failed",
        error: acknowledgement.error,
      },
    ]);

    portalMessage(parent, {
      ...acknowledgement,
      correlation_id: correlationId,
      status: "session-ready",
      session_id: "late-session",
    });
    expect(updates).toHaveLength(1);

    const retryUpdates: import("./postMessageBridge").AiErrorHandoverUpdate[] =
      [];
    expect(
      bridge.requestAiErrorHandover(request, (update) =>
        retryUpdates.push(update),
      ),
    ).toBe(true);
    portalMessage(parent, {
      type: "ai-handover-ack",
      tool: "kombifystack",
      schema: "kombify.ai-handover-ack.v1",
      handover_schema: "kombify.ai-handover.v1",
      correlation_id: correlationId,
      status: "accepted",
    });
    expect(retryUpdates).toEqual([{ status: "accepted" }]);
  });
});
