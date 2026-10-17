/**
 * The Wallet's fresh owner proof. Revealing a value and signing an SSH
 * certificate need a second factor from the last few minutes; the Gateway
 * refuses without one (`403 wallet_step_up_required`). The dialog then sends
 * the owner through Techstack's own Auth0 sign-in (`startGatewayStepUp`:
 * multi-factor `acr_values`, `max_age=0`, `prompt=login`) and the Wallet page
 * reopens the same action on return, so it runs again with the refreshed
 * token. The Gateway still decides every release.
 */
export const WALLET_STEP_UP_REQUIRED = "wallet_step_up_required";
/** The page's query member naming the action to reopen after the step-up. */
export const WALLET_RESUME_PARAM = "wallet_resume";

export type WalletResumeAction = "reveal" | "sign";
export interface WalletResume {
  action: WalletResumeAction;
  itemId: string;
}

const RESUME = /^(reveal|sign)\.(itm_[a-z2-7]{26})$/;
/** One-shot marker: only a step-up this browser started reopens an action. */
const RESUME_MARKER = "kombify.wallet.step-up-resume";
/** Public material of a sign request, kept for the one return. */
const SIGN_STASH = "kombify.wallet.step-up-sign";

export function formatResume(resume: WalletResume): string {
  return `${resume.action}.${resume.itemId}`;
}

/** Null for anything but exactly `<reveal|sign>.<itemId>`. */
export function parseResume(
  value: string | null | undefined,
): WalletResume | null {
  const match = RESUME.exec(value ?? "");
  return match
    ? { action: match[1] as WalletResumeAction, itemId: match[2] }
    : null;
}

/** The same-origin path the step-up returns to: this page, naming the action. */
export function resumeTarget(
  resume: WalletResume,
  location: { pathname: string; search: string },
): string {
  const params = new URLSearchParams(location.search);
  params.set(WALLET_RESUME_PARAM, formatResume(resume));
  return `${location.pathname}?${params.toString()}`;
}

/** Whole minutes of the Gateway's freshness window, or null when it named none. */
export function stepUpMinutes(
  seconds: number | null | undefined,
): number | null {
  return typeof seconds === "number" &&
    Number.isSafeInteger(seconds) &&
    seconds > 0
    ? Math.ceil(seconds / 60)
    : null;
}

function store(): Storage | null {
  try {
    return typeof sessionStorage === "undefined" ? null : sessionStorage;
  } catch {
    return null;
  }
}

/** Record, right before the step-up, the one action its return may reopen. */
export function markResume(resume: WalletResume): void {
  try {
    store()?.setItem(RESUME_MARKER, formatResume(resume));
  } catch {
    // Without storage the owner repeats the action by hand after the step-up.
  }
}

/** True once, when this browser started a step-up for exactly this action;
 * the marker is removed either way, so a crafted link reopens nothing. */
export function takeResumeMarker(resume: WalletResume): boolean {
  try {
    const storage = store();
    const marked = storage?.getItem(RESUME_MARKER);
    storage?.removeItem(RESUME_MARKER);
    return marked === formatResume(resume);
  } catch {
    return false;
  }
}

/**
 * Start the step-up for `resume`. The marker is set first and dropped again
 * when no sign-in started (`start` says so), so a failed start leaves no
 * stale permission behind.
 */
export async function beginStepUp(
  resume: WalletResume,
  start: (returnTo: string) => Promise<boolean>,
  location: { pathname: string; search: string },
): Promise<boolean> {
  markResume(resume);
  let started = false;
  try {
    started = await start(resumeTarget(resume, location));
  } finally {
    if (!started) {
      try {
        store()?.removeItem(RESUME_MARKER);
        store()?.removeItem(SIGN_STASH);
      } catch {
        // Nothing stored, nothing to drop.
      }
    }
  }
  return started;
}

/**
 * The action a returned URL may reopen. Reads and clears the marker, so a
 * link that this browser did not start a step-up for reopens nothing.
 */
export function resumeFromUrl(url: URL): WalletResume | null {
  const resume = parseResume(url.searchParams.get(WALLET_RESUME_PARAM));
  return resume && takeResumeMarker(resume) ? resume : null;
}

/** The URL without the resume member, for `replaceState`; null when absent. */
export function withoutResume(url: URL): URL | null {
  if (!url.searchParams.has(WALLET_RESUME_PARAM)) return null;
  const next = new URL(url);
  next.searchParams.delete(WALLET_RESUME_PARAM);
  return next;
}

export interface SignStash {
  publicKey: string;
  kind: string;
  principals: string;
  ttl: string;
  sourceAddress: string;
}

const STASH_FIELDS = [
  "publicKey",
  "kind",
  "principals",
  "ttl",
  "sourceAddress",
] as const;

/** Keep the public sign request for the one return of this step-up. */
export function stashSign(itemId: string, draft: SignStash): void {
  try {
    store()?.setItem(
      SIGN_STASH,
      JSON.stringify({
        itemId,
        draft: Object.fromEntries(STASH_FIELDS.map((k) => [k, draft[k]])),
      }),
    );
  } catch {
    // The owner fills the request in again.
  }
}

/** The stashed sign request for this item, once; null for anything else. */
export function takeSignStash(itemId: string): SignStash | null {
  try {
    const storage = store();
    const raw = storage?.getItem(SIGN_STASH);
    storage?.removeItem(SIGN_STASH);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as { itemId?: unknown; draft?: unknown };
    const draft = parsed.draft as Record<string, unknown> | null;
    if (parsed.itemId !== itemId || !draft || typeof draft !== "object")
      return null;
    for (const key of STASH_FIELDS)
      if (typeof draft[key] !== "string" || draft[key].length > 16_384)
        return null;
    return draft as unknown as SignStash;
  } catch {
    return null;
  }
}
