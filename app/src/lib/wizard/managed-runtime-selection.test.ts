import { describe, expect, it } from "vitest";
import { shouldDowngradeManagedRuntimeSelection } from "./managed-runtime-selection";

describe("shouldDowngradeManagedRuntimeSelection", () => {
  const conclusiveDenial = {
    authenticated: true,
    featuresReady: true,
    featuresLoading: false,
    verificationFailed: false,
    selectable: false,
  };

  it("preserves the managed choice while embedded authentication is pending", () => {
    expect(
      shouldDowngradeManagedRuntimeSelection({
        ...conclusiveDenial,
        authenticated: false,
      }),
    ).toBe(false);
  });

  it("preserves the managed choice during loading and retryable verification failures", () => {
    expect(
      shouldDowngradeManagedRuntimeSelection({
        ...conclusiveDenial,
        featuresReady: false,
        featuresLoading: true,
      }),
    ).toBe(false);
    expect(
      shouldDowngradeManagedRuntimeSelection({
        ...conclusiveDenial,
        verificationFailed: true,
      }),
    ).toBe(false);
  });

  it("downgrades only after a conclusive authenticated denial", () => {
    expect(shouldDowngradeManagedRuntimeSelection(conclusiveDenial)).toBe(true);
    expect(
      shouldDowngradeManagedRuntimeSelection({
        ...conclusiveDenial,
        selectable: true,
      }),
    ).toBe(false);
  });
});
