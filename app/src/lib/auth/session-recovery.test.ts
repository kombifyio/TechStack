import { beforeEach, describe, expect, it, vi } from "vitest";

const sentryState = vi.hoisted(() => {
  const scope = {
    setLevel: vi.fn(),
    setTag: vi.fn(),
  };
  return {
    captureMessage: vi.fn(),
    scope,
    withScope: vi.fn((callback: (scope: unknown) => void) => {
      callback(scope);
    }),
  };
});

vi.mock("@sentry/sveltekit", () => sentryState);

import {
  AUTO_RELOGIN_MARKER_KEY,
  AUTO_RELOGIN_TTL_MS,
  classifyAuthFailure,
  clearAutoReloginMarker,
  isGatewayAuthFailure,
  markAutoReloginAttempt,
  noteGatewayTokenSuccess,
  shouldAttemptAutoRelogin,
} from "./session-recovery";

function fakeStorage(initial: Record<string, string> = {}): Storage {
  const map = new Map(Object.entries(initial));
  return {
    get length() {
      return map.size;
    },
    clear: () => map.clear(),
    getItem: (key: string) => map.get(key) ?? null,
    key: (index: number) => [...map.keys()][index] ?? null,
    removeItem: (key: string) => {
      map.delete(key);
    },
    setItem: (key: string, value: string) => {
      map.set(key, value);
    },
  };
}

describe("isGatewayAuthFailure", () => {
  it("matches the typed ApiRequestError codes", () => {
    expect(
      isGatewayAuthFailure({ status: 401, code: "gateway_auth_unavailable" }),
    ).toBe(true);
  });

  it("matches Auth0 SPA silent-auth error shapes", () => {
    expect(isGatewayAuthFailure({ error: "login_required" })).toBe(true);
    expect(isGatewayAuthFailure({ error: "missing_refresh_token" })).toBe(true);
  });

  it("matches the typed Error messages thrown by gateway-auth", () => {
    expect(isGatewayAuthFailure(new Error("gateway_auth_unavailable"))).toBe(
      true,
    );
  });

  it("rejects plain 401s and unrelated errors", () => {
    expect(isGatewayAuthFailure({ status: 401 })).toBe(false);
    expect(isGatewayAuthFailure(new Error("boom"))).toBe(false);
    expect(isGatewayAuthFailure(null)).toBe(false);
    expect(isGatewayAuthFailure("gateway_auth_unavailable")).toBe(false);
  });
});

describe("classifyAuthFailure", () => {
  const gatewayError = { status: 401, code: "gateway_auth_unavailable" };

  it("is session_dead whenever the browser session is inactive", () => {
    expect(classifyAuthFailure(gatewayError, { v2SessionActive: false })).toBe(
      "session_dead",
    );
    expect(
      classifyAuthFailure({ status: 401 }, { v2SessionActive: false }),
    ).toBe("session_dead");
  });

  it("is gateway_token for gateway failures with an alive session", () => {
    expect(classifyAuthFailure(gatewayError, { v2SessionActive: true })).toBe(
      "gateway_token",
    );
  });

  it("is generic_401 otherwise", () => {
    expect(
      classifyAuthFailure({ status: 401 }, { v2SessionActive: true }),
    ).toBe("generic_401");
  });
});

describe("auto-relogin loop guard", () => {
  it("allows the attempt when no marker exists", () => {
    expect(shouldAttemptAutoRelogin(1_000_000, fakeStorage())).toBe(true);
  });

  it("blocks while the marker is fresh and allows after the TTL", () => {
    const storage = fakeStorage();
    markAutoReloginAttempt(1_000_000, storage);
    expect(shouldAttemptAutoRelogin(1_000_000 + 5_000, storage)).toBe(false);
    expect(
      shouldAttemptAutoRelogin(1_000_000 + AUTO_RELOGIN_TTL_MS + 1, storage),
    ).toBe(true);
  });

  it("tolerates a corrupted marker value", () => {
    const storage = fakeStorage({ [AUTO_RELOGIN_MARKER_KEY]: "not-a-number" });
    expect(shouldAttemptAutoRelogin(1_000_000, storage)).toBe(true);
  });

  it("clears the marker", () => {
    const storage = fakeStorage();
    markAutoReloginAttempt(1_000_000, storage);
    clearAutoReloginMarker(storage);
    expect(storage.getItem(AUTO_RELOGIN_MARKER_KEY)).toBeNull();
  });
});

describe("noteGatewayTokenSuccess", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("emits the recovered event and clears a pending marker", () => {
    const storage = fakeStorage();
    markAutoReloginAttempt(1_000_000, storage);

    noteGatewayTokenSuccess(storage);

    expect(storage.getItem(AUTO_RELOGIN_MARKER_KEY)).toBeNull();
    expect(sentryState.captureMessage).toHaveBeenCalledWith(
      "techstack_session_auto_relogin_recovered",
    );
  });

  it("does nothing without a pending marker", () => {
    noteGatewayTokenSuccess(fakeStorage());
    expect(sentryState.captureMessage).not.toHaveBeenCalled();
  });
});
