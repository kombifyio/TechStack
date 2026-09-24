/**
 * The INTRODUCTION: onboarding phase one for Techstack.
 *
 * Hints, not tasks. It runs once on the first visit, points at chrome that
 * already exists, and completes nothing — Getting started (phase two) is the
 * one that tracks what the user did. The two never read each other's state,
 * which is why the stop list below is declared by hand instead of derived
 * from the journey.
 *
 * The lifecycle is the product's (device-local, like Workbench's `wb-tour-v1`
 * and CMO's own record). The package owns only the presentation.
 */
import type { IntroductionStop } from "@kombiverselabs/ui/onboarding";

/** Device-local, swept with the rest of the session on sign-out. */
export const INTRODUCTION_STORAGE_KEY = "techstack-cache:introduction";

/**
 * Bumped when the stops change materially. A higher version is reachable
 * through the user menu only: it must never re-interrupt somebody who already
 * finished or dismissed an older introduction.
 */
export const INTRODUCTION_VERSION = 2;

/** Fired by the user menu; the mounted host listens and starts. */
export const INTRODUCTION_START_EVENT = "techstack-introduction-start";

export interface IntroductionRecord {
  /** Content version the user saw. An absent record means "never seen". */
  seenVersion: number;
  completedAt?: string;
  dismissedAt?: string;
  dismissedAtStop?: string;
}

/**
 * The stops. Anchors name elements that exist in the shell today; a stop whose
 * anchor never appears is skipped rather than shown pointing at nothing.
 */
export const TECHSTACK_INTRODUCTION: IntroductionStop[] = [
  {
    // The welcome is NOT a stop. It asks for the homelab's name, which is a
    // product decision written to stack identity, not a hint about chrome —
    // so `WelcomeDialog` owns it and the spotlight tour starts after it.
    id: "navigation",
    // The whole nav, not one item in it. The stop is about the map, and a
    // single-item anchor puts the card in the strip beside the rail — exactly
    // where a hover flyout opens, which is the thing the copy asks the reader
    // to try. Anchoring the nav itself moves the card clear of that lane.
    anchor: "techstack-nav",
    placement: "below",
    messageId: "introduction.navigation.title",
    bodyMessageId: "introduction.navigation.body",
  },
  {
    id: "getting-started",
    anchor: "getting-started",
    placement: "end",
    messageId: "introduction.getting_started.title",
    bodyMessageId: "introduction.getting_started.body",
  },
  {
    id: "account",
    anchor: "user-menu",
    placement: "end",
    messageId: "introduction.account.title",
    bodyMessageId: "introduction.account.body",
  },
];

function storage(): Storage | undefined {
  return typeof window === "undefined" ? undefined : window.localStorage;
}

export function readIntroduction(
  store: Storage | undefined = storage(),
): IntroductionRecord | undefined {
  try {
    const raw = store?.getItem(INTRODUCTION_STORAGE_KEY);
    if (!raw) return undefined;
    const parsed = JSON.parse(raw) as Partial<IntroductionRecord>;
    if (typeof parsed.seenVersion !== "number") return undefined;
    return {
      seenVersion: parsed.seenVersion,
      completedAt:
        typeof parsed.completedAt === "string" ? parsed.completedAt : undefined,
      dismissedAt:
        typeof parsed.dismissedAt === "string" ? parsed.dismissedAt : undefined,
      dismissedAtStop:
        typeof parsed.dismissedAtStop === "string"
          ? parsed.dismissedAtStop
          : undefined,
    };
  } catch {
    return undefined;
  }
}

function write(record: IntroductionRecord, store = storage()): void {
  try {
    store?.setItem(INTRODUCTION_STORAGE_KEY, JSON.stringify(record));
  } catch {
    // Storage unavailable: the introduction just does not persist this session.
  }
}

/**
 * Written when the introduction STARTS, not when it finishes. A reload halfway
 * through must not put the user back at stop one forever.
 */
export function markIntroductionStarted(store = storage()): void {
  write(
    { ...readIntroduction(store), seenVersion: INTRODUCTION_VERSION },
    store,
  );
}

export function markIntroductionCompleted(store = storage()): void {
  write(
    {
      seenVersion: INTRODUCTION_VERSION,
      completedAt: new Date().toISOString(),
    },
    store,
  );
}

/** The stop the user left at is the measurement worth keeping. */
export function markIntroductionDismissed(
  stopId: string,
  store = storage(),
): void {
  write(
    {
      seenVersion: INTRODUCTION_VERSION,
      dismissedAt: new Date().toISOString(),
      dismissedAtStop: stopId,
    },
    store,
  );
}

export function forgetIntroduction(store = storage()): void {
  try {
    store?.removeItem(INTRODUCTION_STORAGE_KEY);
  } catch {
    // Nothing to do.
  }
}

/**
 * Auto-start only on a TRUE first visit: never seen the introduction, and a
 * Getting started list nobody has touched. Somebody with progress already
 * found their way around; interrupting them is not onboarding, it is a popup.
 * They reach it from the user menu.
 */
export function shouldAutoStartIntroduction(
  record: IntroductionRecord | undefined,
  gettingStarted: { completedCount: number; dismissed: boolean } | null,
): boolean {
  if (record) return false;
  if (!gettingStarted) return false;
  return gettingStarted.completedCount === 0 && !gettingStarted.dismissed;
}

/**
 * "Keep this small" is the user's own choice and has to survive a reload;
 * dismissing is a different intent and lives on the server with the journey.
 * Device-local is right for a display preference — it is per browser, not per
 * principal — and the key sits in the sweep that clears on sign-out.
 */
export const GETTING_STARTED_MINIMIZED_KEY =
  "techstack-cache:getting-started-minimized";

export function readGettingStartedMinimized(store = storage()): boolean {
  try {
    return store?.getItem(GETTING_STARTED_MINIMIZED_KEY) === "1";
  } catch {
    return false;
  }
}

export function writeGettingStartedMinimized(
  minimized: boolean,
  store = storage(),
): void {
  try {
    if (minimized) store?.setItem(GETTING_STARTED_MINIMIZED_KEY, "1");
    else store?.removeItem(GETTING_STARTED_MINIMIZED_KEY);
  } catch {
    // A display preference is not worth failing a render over.
  }
}

export function requestIntroductionStart(): void {
  if (typeof window === "undefined") return;
  window.dispatchEvent(new CustomEvent(INTRODUCTION_START_EVENT));
}
