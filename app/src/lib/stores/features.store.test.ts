import { beforeEach, describe, expect, it, vi } from "vitest";
import { get } from "svelte/store";

const { listFeaturesMock } = vi.hoisted(() => ({
  listFeaturesMock: vi.fn(),
}));

vi.mock("$lib/api/features", () => ({
  listFeatures: listFeaturesMock,
}));

import { features } from "./features";
import { ApiRequestError } from "$lib/api/client";

const EMPTY_FEATURES = { security: [], beta: [], ux: [] };

describe("features store runtime gating", () => {
  beforeEach(() => {
    features.reset();
    listFeaturesMock.mockReset();
  });

  it("falls back to secure defaults before the first backend load", () => {
    expect(features.isEnabled("cloud_backup")).toBe(false);
    expect(features.isEnabled("monthly_runtime")).toBe(false);
    expect(features.isEnabled("monthly_runtime_cloudkit")).toBe(false);
    expect(features.isEnabled("monthly_runtime_centron")).toBe(false);
    expect(features.isEnabled("dark_mode")).toBe(true);
  });

  it("uses the loaded backend states after the store becomes ready", async () => {
    listFeaturesMock.mockResolvedValue({
      security: [
        {
          key: "cloud_backup",
          name: "Cloud Backup",
          enabled: true,
          locked: false,
          requires_consent: false,
          has_consent: false,
          risk_level: "medium",
          description: "",
          category: "security",
        },
      ],
      beta: [],
      ux: [
        {
          key: "dark_mode",
          name: "Dark Mode",
          enabled: false,
          locked: false,
          requires_consent: false,
          has_consent: false,
          risk_level: "low",
          description: "",
          category: "ux",
        },
      ],
    });

    await features.load(true);

    expect(features.isEnabled("cloud_backup")).toBe(true);
    expect(features.isEnabled("dark_mode")).toBe(false);
  });
});

describe("features store error classification", () => {
  beforeEach(() => {
    features.reset();
    listFeaturesMock.mockReset();
  });

  it("classifies a 401 as a retryable auth error (not a not-entitled state)", async () => {
    listFeaturesMock.mockRejectedValue(
      new ApiRequestError("Authentication required", { status: 401 }),
    );

    await features.load(true);

    expect(get(features.errorKind)).toBe("auth");
    expect(get(features.error)).toBeTruthy();
    // The store never flips to "ready" on failure, so the wizard keeps managed
    // disabled fail-closed rather than reporting positive entitlement.
    expect(get(features.ready)).toBe(false);
  });

  it("classifies a 403 as an auth error", async () => {
    listFeaturesMock.mockRejectedValue(
      new ApiRequestError("Forbidden", { status: 403 }),
    );

    await features.load(true);

    expect(get(features.errorKind)).toBe("auth");
  });

  it("classifies a 5xx as a server error", async () => {
    listFeaturesMock.mockRejectedValue(
      new ApiRequestError("Internal", { status: 503 }),
    );

    await features.load(true);

    expect(get(features.errorKind)).toBe("server");
  });

  it("classifies a status-less failure as a network error", async () => {
    listFeaturesMock.mockRejectedValue(new ApiRequestError("Network down"));

    await features.load(true);

    expect(get(features.errorKind)).toBe("network");
  });

  it("clears the error and errorKind on a successful reload", async () => {
    listFeaturesMock.mockRejectedValueOnce(
      new ApiRequestError("Authentication required", { status: 401 }),
    );
    await features.load(true);
    expect(get(features.errorKind)).toBe("auth");

    listFeaturesMock.mockResolvedValueOnce(EMPTY_FEATURES);
    await features.load(true);

    expect(get(features.errorKind)).toBeNull();
    expect(get(features.error)).toBeNull();
    expect(get(features.ready)).toBe(true);
  });
});
