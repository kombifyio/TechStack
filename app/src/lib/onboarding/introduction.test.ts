import { describe, expect, it } from "vitest";

import {
  INTRODUCTION_VERSION,
  markIntroductionCompleted,
  markIntroductionDismissed,
  readIntroduction,
  shouldAutoStartIntroduction,
} from "./introduction.js";

function memoryStorage(): Storage {
  const map = new Map<string, string>();
  return {
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => void map.set(k, v),
    removeItem: (k: string) => void map.delete(k),
    clear: () => map.clear(),
    key: () => null,
    length: 0,
  } as unknown as Storage;
}

const untouched = { completedCount: 0, dismissed: false };

describe("introduction auto-start", () => {
  it("runs for a principal who has never seen it and done nothing", () => {
    expect(shouldAutoStartIntroduction(undefined, untouched)).toBe(true);
  });

  // The single rule that decides whether we interrupt somebody: a person with
  // progress already found their way around.
  it("does not run for somebody who already completed a task", () => {
    expect(
      shouldAutoStartIntroduction(undefined, {
        completedCount: 1,
        dismissed: false,
      }),
    ).toBe(false);
  });

  it("does not run for somebody who hid the checklist", () => {
    expect(
      shouldAutoStartIntroduction(undefined, {
        completedCount: 0,
        dismissed: true,
      }),
    ).toBe(false);
  });

  it("never runs again once it has been seen, completed or dismissed", () => {
    const seen = { seenVersion: INTRODUCTION_VERSION };
    expect(shouldAutoStartIntroduction(seen, untouched)).toBe(false);
  });

  // Waiting for the journey is the reason the host defers the decision; a null
  // reading must not be read as "brand new".
  it("does not run while the journey is still unknown", () => {
    expect(shouldAutoStartIntroduction(undefined, null)).toBe(false);
  });

  it("remembers which stop the user left at", () => {
    const store = memoryStorage();
    markIntroductionDismissed("navigation", store);
    expect(readIntroduction(store)?.dismissedAtStop).toBe("navigation");
  });

  it("records completion", () => {
    const store = memoryStorage();
    markIntroductionCompleted(store);
    const record = readIntroduction(store);
    expect(record?.completedAt).toBeTruthy();
    expect(record?.seenVersion).toBe(INTRODUCTION_VERSION);
  });
});
