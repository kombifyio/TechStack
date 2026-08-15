import { describe, expect, it, vi } from "vitest";

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
  REPROJECTION_REAUTH_MARKER_KEY,
  REPROJECTION_REAUTH_TTL_MS,
  SESSION_REPROJECTION_REASON_CODE,
  isSessionReprojectionError,
  markReprojectionReauthAttempt,
  recoverFromSessionReprojection,
  shouldAttemptReprojectionReauth,
  type SessionReprojectionDeps,
} from "./session-reprojection";

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

type MockedReprojectionDeps = SessionReprojectionDeps & {
  refreshEmbeddedSession: ReturnType<typeof vi.fn<() => Promise<boolean>>>;
  redirectToSSO: ReturnType<typeof vi.fn<() => Promise<boolean>>>;
  promptRenewSession: ReturnType<typeof vi.fn<(cause: unknown) => void>>;
};

function makeDeps(
  overrides: Partial<SessionReprojectionDeps> = {},
): MockedReprojectionDeps {
  return {
    isEmbedded: () => false,
    refreshEmbeddedSession: vi
      .fn<() => Promise<boolean>>()
      .mockResolvedValue(true),
    redirectToSSO: vi.fn<() => Promise<boolean>>().mockResolvedValue(true),
    promptRenewSession: vi.fn<(cause: unknown) => void>(),
    now: () => 1_000_000,
    storage: fakeStorage(),
    ...overrides,
  } as MockedReprojectionDeps;
}

const reprojection401 = {
  status: 401,
  code: "UNAUTHORIZED",
  details: { reason_code: SESSION_REPROJECTION_REASON_CODE, retryable: true },
};

describe("isSessionReprojectionError", () => {
  it("matches the server envelope (401 + reason_code in details)", () => {
    expect(isSessionReprojectionError(reprojection401)).toBe(true);
    expect(
      isSessionReprojectionError({
        status: 401,
        details: { error_code: SESSION_REPROJECTION_REASON_CODE },
      }),
    ).toBe(true);
  });

  it("rejects other 401s and other statuses carrying the reason code", () => {
    expect(isSessionReprojectionError({ status: 401 })).toBe(false);
    expect(
      isSessionReprojectionError({
        status: 401,
        details: { reason_code: "tenant_context_missing" },
      }),
    ).toBe(false);
    expect(
      isSessionReprojectionError({
        status: 403,
        details: { reason_code: SESSION_REPROJECTION_REASON_CODE },
      }),
    ).toBe(false);
    expect(isSessionReprojectionError(null)).toBe(false);
  });
});

describe("reprojection re-auth loop guard", () => {
  it("allows exactly one automatic attempt per TTL window", () => {
    const storage = fakeStorage();
    expect(shouldAttemptReprojectionReauth(1_000_000, storage)).toBe(true);
    markReprojectionReauthAttempt(1_000_000, storage);
    expect(storage.getItem(REPROJECTION_REAUTH_MARKER_KEY)).toBe("1000000");
    expect(shouldAttemptReprojectionReauth(1_000_000 + 5_000, storage)).toBe(
      false,
    );
    expect(
      shouldAttemptReprojectionReauth(
        1_000_000 + REPROJECTION_REAUTH_TTL_MS + 1,
        storage,
      ),
    ).toBe(true);
  });
});

describe("recoverFromSessionReprojection (standalone)", () => {
  it("runs exactly one silent SSO redirect and honors the loop guard", async () => {
    const storage = fakeStorage();
    const deps = makeDeps({ storage });

    const first = await recoverFromSessionReprojection(reprojection401, deps);
    expect(first).toBe("redirecting");
    expect(deps.redirectToSSO).toHaveBeenCalledTimes(1);
    expect(deps.promptRenewSession).not.toHaveBeenCalled();

    // Second occurrence within the 5-minute window: no second automatic
    // attempt - raise the single-click renew action instead.
    const second = await recoverFromSessionReprojection(reprojection401, deps);
    expect(second).toBe("reauth_required");
    expect(deps.redirectToSSO).toHaveBeenCalledTimes(1);
    expect(deps.promptRenewSession).toHaveBeenCalledTimes(1);
  });

  it("coalesces concurrent occurrences into a single attempt", async () => {
    const deps = makeDeps();
    let release: (value: boolean) => void = () => {};
    deps.redirectToSSO.mockImplementation(
      () => new Promise<boolean>((resolve) => (release = resolve)),
    );

    const firstCall = recoverFromSessionReprojection(reprojection401, deps);
    const secondCall = recoverFromSessionReprojection(reprojection401, deps);
    release(true);
    await expect(firstCall).resolves.toBe("redirecting");
    await expect(secondCall).resolves.toBe("redirecting");
    expect(deps.redirectToSSO).toHaveBeenCalledTimes(1);
  });

  it("falls back to the renew-session action when the redirect is unavailable", async () => {
    const deps = makeDeps();
    deps.redirectToSSO.mockResolvedValue(false);

    const outcome = await recoverFromSessionReprojection(reprojection401, deps);
    expect(outcome).toBe("reauth_required");
    expect(deps.promptRenewSession).toHaveBeenCalledTimes(1);
  });
});

describe("recoverFromSessionReprojection (embedded)", () => {
  it("signals the host instead of redirecting and replays the request", async () => {
    const deps = makeDeps({ isEmbedded: () => true });

    const outcome = await recoverFromSessionReprojection(reprojection401, deps);
    expect(outcome).toBe("retry");
    expect(deps.refreshEmbeddedSession).toHaveBeenCalledWith({
      forcePortalVerify: true,
    });
    expect(deps.redirectToSSO).not.toHaveBeenCalled();
    expect(deps.promptRenewSession).not.toHaveBeenCalled();
  });

  it("raises the renew-session action when the host refresh fails", async () => {
    const deps = makeDeps({ isEmbedded: () => true });
    deps.refreshEmbeddedSession.mockResolvedValue(false);

    const outcome = await recoverFromSessionReprojection(reprojection401, deps);
    expect(outcome).toBe("reauth_required");
    expect(deps.redirectToSSO).not.toHaveBeenCalled();
    expect(deps.promptRenewSession).toHaveBeenCalledTimes(1);
  });
});
