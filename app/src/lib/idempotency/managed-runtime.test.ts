// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AddManagedRuntimeServerRequest } from "$lib/api/stacks";
import {
  canonicalizeManagedRuntimeIntent,
  getOrCreateManagedRuntimeIdempotency,
  settleManagedRuntimeIdempotency,
} from "./managed-runtime";

const baseRequest: AddManagedRuntimeServerRequest = {
  provider_id: "ionos",
  ionos_datacenter: "de/fra",
  provider_region: "de/fra",
  node_role: "worker",
  runtime_offering_id: "monthly-runtime-standard",
  stackkit: "cloud-kit",
  services: ["monitoring", "files"],
};

describe("managed runtime browser idempotency", () => {
  beforeEach(() => {
    window.sessionStorage.clear();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("canonicalizes the complete secret-free managed runtime intent", () => {
    expect(
      canonicalizeManagedRuntimeIntent(" stack-123 ", {
        provider_id: " IONOS " as "ionos",
        ionos_datacenter: " Berlin ",
        provider_region: "us/ewr",
        node_role: " MAIN ",
        runtime_offering_id: " MONTHLY-RUNTIME-PREMIUM ",
        stackkit: " CloudKit ",
        services: [
          " pocket-id ",
          "identity",
          "FILES",
          "files",
          "monitoring",
          "",
        ],
      }),
    ).toEqual({
      stack_id: "stack-123",
      provider_id: "ionos",
      provider_region: "de/txl",
      node_role: "foundation",
      runtime_offering_id: "monthly-runtime-premium",
      stackkit: "cloud-kit",
      services: ["files", "monitoring", "pocket_id"],
    });
  });

  it("reuses one opaque key over reload, timeout, and double-submit equivalents", () => {
    const createKey = vi.fn(() => "opaque-key-1");
    const first = getOrCreateManagedRuntimeIdempotency(
      window.sessionStorage,
      "stack-123",
      baseRequest,
      createKey,
    );
    const replay = getOrCreateManagedRuntimeIdempotency(
      window.sessionStorage,
      "stack-123",
      {
        ...baseRequest,
        ionos_datacenter: "frankfurt",
        provider_region: undefined,
        stackkit: "cloudkit",
        services: [" files ", "monitoring", "FILES"],
      },
      createKey,
    );

    expect(replay.key).toBe(first.key);
    expect(createKey).toHaveBeenCalledTimes(1);
    expect(window.sessionStorage.length).toBe(1);
    expect(window.sessionStorage.key(0)).toMatch(
      /^creating:add-server:stack-123:/,
    );

    settleManagedRuntimeIdempotency(window.sessionStorage, replay, {
      status: 0,
    });
    settleManagedRuntimeIdempotency(window.sessionStorage, replay, {
      status: 503,
    });
    settleManagedRuntimeIdempotency(window.sessionStorage, replay, {
      status: 409,
      retryable: true,
    });
    settleManagedRuntimeIdempotency(window.sessionStorage, replay, {
      status: 202,
      retryable: true,
    });
    expect(window.sessionStorage.length).toBe(1);
  });

  it("generates new browser keys with crypto.randomUUID", () => {
    const randomUUID = vi.fn(() => "11111111-1111-4111-8111-111111111111");
    vi.stubGlobal("crypto", { randomUUID });

    const attempt = getOrCreateManagedRuntimeIdempotency(
      window.sessionStorage,
      "stack-123",
      baseRequest,
    );

    expect(randomUUID).toHaveBeenCalledOnce();
    expect(attempt.key).toBe("11111111-1111-4111-8111-111111111111");
  });

  it.each([
    ["provider", { provider_id: "centron" }],
    ["IONOS region", { ionos_datacenter: "de/txl" }],
    ["node role", { node_role: "storage" }],
    ["offering", { runtime_offering_id: "monthly-runtime-premium" }],
    ["StackKit", { stackkit: "basement-kit" }],
    ["services", { services: ["files"] }],
  ])("rotates the key when the semantic %s changes", (_label, change) => {
    const createKey = vi
      .fn<() => string>()
      .mockReturnValueOnce("opaque-key-1")
      .mockReturnValueOnce("opaque-key-2");
    const first = getOrCreateManagedRuntimeIdempotency(
      window.sessionStorage,
      "stack-123",
      baseRequest,
      createKey,
    );
    const changed = getOrCreateManagedRuntimeIdempotency(
      window.sessionStorage,
      "stack-123",
      { ...baseRequest, ...change } as AddManagedRuntimeServerRequest,
      createKey,
    );

    expect(first.key).toBe("opaque-key-1");
    expect(changed.key).toBe("opaque-key-2");
  });

  it.each([
    { outcome: { status: 202 }, label: "accepted handoff" },
    { outcome: { status: 422 }, label: "terminal validation" },
    {
      outcome: { status: 403, retryable: false },
      label: "terminal capacity denial",
    },
    {
      outcome: { status: 409, retryable: false },
      label: "terminal conflict",
    },
    {
      outcome: { status: 409 },
      label: "conflict without a retryable marker",
    },
  ])("clears the key after $label", ({ outcome }) => {
    const attempt = getOrCreateManagedRuntimeIdempotency(
      window.sessionStorage,
      "stack-123",
      baseRequest,
      () => "opaque-key-1",
    );

    settleManagedRuntimeIdempotency(window.sessionStorage, attempt, outcome);

    expect(window.sessionStorage.length).toBe(0);
  });

  it("does not let a late settlement clear a newer semantic intent", () => {
    const oldAttempt = getOrCreateManagedRuntimeIdempotency(
      window.sessionStorage,
      "stack-123",
      baseRequest,
      () => "opaque-key-1",
    );
    const newAttempt = getOrCreateManagedRuntimeIdempotency(
      window.sessionStorage,
      "stack-123",
      { ...baseRequest, node_role: "storage" },
      () => "opaque-key-2",
    );

    settleManagedRuntimeIdempotency(window.sessionStorage, oldAttempt, {
      status: 202,
    });
    const replay = getOrCreateManagedRuntimeIdempotency(
      window.sessionStorage,
      "stack-123",
      { ...baseRequest, node_role: "storage" },
      () => "unexpected-key",
    );

    expect(replay.key).toBe(newAttempt.key);
  });
});
