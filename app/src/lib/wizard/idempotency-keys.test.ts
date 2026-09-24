/**
 * @vitest-environment jsdom
 */
import { describe, expect, it, beforeEach } from "vitest";
import {
  clearJoinWizardIdempotencyKeys,
  joinWizardIdempotencyStorageKey,
  legacyJoinWizardIdempotencyStorageKey,
  mintJoinWizardIdempotencyKey,
  readJoinWizardIdempotencyKey,
} from "./idempotency-keys";

describe("join wizard idempotency keys", () => {
  beforeEach(() => {
    window.sessionStorage.clear();
  });

  it("stores join and found keys separately per stack", () => {
    const joinKey = mintJoinWizardIdempotencyKey("stack-1", "join");
    const foundKey = mintJoinWizardIdempotencyKey("stack-1", "found");
    expect(joinKey).not.toBe(foundKey);
    expect(readJoinWizardIdempotencyKey("stack-1", "join")).toBe(joinKey);
    expect(readJoinWizardIdempotencyKey("stack-1", "found")).toBe(foundKey);
  });

  it("clears both join and found keys for a stack", () => {
    mintJoinWizardIdempotencyKey("stack-2", "join");
    mintJoinWizardIdempotencyKey("stack-2", "found");
    window.sessionStorage.setItem(
      legacyJoinWizardIdempotencyStorageKey("stack-2"),
      "legacy-key",
    );
    clearJoinWizardIdempotencyKeys("stack-2");
    expect(
      window.sessionStorage.getItem(
        joinWizardIdempotencyStorageKey("stack-2", "join"),
      ),
    ).toBeNull();
    expect(
      window.sessionStorage.getItem(
        joinWizardIdempotencyStorageKey("stack-2", "found"),
      ),
    ).toBeNull();
    expect(
      window.sessionStorage.getItem(
        legacyJoinWizardIdempotencyStorageKey("stack-2"),
      ),
    ).toBeNull();
  });

  it("migrates legacy keys into the mode-scoped storage shape", () => {
    window.sessionStorage.setItem(
      legacyJoinWizardIdempotencyStorageKey("stack-legacy"),
      "legacy-attempt-key",
    );
    expect(readJoinWizardIdempotencyKey("stack-legacy", "join")).toBe(
      "legacy-attempt-key",
    );
    expect(
      window.sessionStorage.getItem(
        legacyJoinWizardIdempotencyStorageKey("stack-legacy"),
      ),
    ).toBeNull();
  });
});
