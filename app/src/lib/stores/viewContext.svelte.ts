/**
 * The view context the frame publishes to its host (see `#lib/view-context.ts`).
 *
 * The root layout names the page (route and English title) on every
 * navigation; a page adds the object it shows with `useViewContextOverlay`
 * for as long as it is mounted. Everything is published through the postMessage
 * bridge, only inside a host frame, and never more often than a burst of
 * changes needs.
 */
import { onDestroy } from "svelte";
import {
  onHostRefresh,
  onViewContextRequest,
  publishViewContext,
} from "#lib/stores/postMessageBridge.js";
import {
  titleForRoute,
  sanitizeViewContext,
  type ViewContextBody,
  type ViewContextDetails,
  type ViewContextInput,
} from "#lib/view-context.js";

// A navigation mounts the new page and unmounts the old one in separate steps;
// the window lets both settle so the host sees the new page once.
const COALESCE_MS = 50;

let route: string | null = null;
const overlays = new Map<symbol, ViewContextDetails | null>();
const subscribers = new Set<(view: ViewContextBody | null) => void>();
let timer: ReturnType<typeof setTimeout> | null = null;

function compose(): ViewContextInput | null {
  if (route === null) return null;
  const input: ViewContextInput = { route, title: titleForRoute(route) };
  // Overlays apply in mount order; a later one wins per field, facts merge.
  for (const overlay of overlays.values()) {
    if (!overlay) continue;
    if (overlay.state !== undefined) input.state = overlay.state;
    if (overlay.subject) input.subject = overlay.subject;
    if (overlay.facts) input.facts = { ...input.facts, ...overlay.facts };
  }
  return input;
}

function flush(options: { force?: boolean; cleared?: boolean } = {}): void {
  if (timer) {
    clearTimeout(timer);
    timer = null;
  }
  const input = compose();
  if (input) publishViewContext(input, options);
  const view = options.cleared ? null : currentViewContext();
  for (const subscriber of subscribers) subscriber(view);
}

function schedule(): void {
  if (typeof window === "undefined" || timer) return;
  timer = setTimeout(() => {
    timer = null;
    flush();
  }, COALESCE_MS);
}

/** The same sanitized page is consumed by standalone Companion and Cloud. */
export function currentViewContext(): ViewContextBody | null {
  const input = compose();
  return input ? sanitizeViewContext(input) : null;
}

export function subscribeViewContext(
  subscriber: (view: ViewContextBody | null) => void,
): () => void {
  subscribers.add(subscriber);
  subscriber(currentViewContext());
  return () => subscribers.delete(subscriber);
}

/** The layout calls this with the pathname of every page the user lands on. */
export function setViewContextRoute(pathname: string): void {
  route = pathname;
  schedule();
}

/**
 * Add what a page knows about the object on screen. Call it while the page
 * component initializes: `read` is tracked, so the overlay follows the page's
 * state, and it is dropped when the page unmounts. Return null while there is
 * nothing to add yet.
 */
export function useViewContextOverlay(
  read: () => ViewContextDetails | null | undefined,
): void {
  const key = Symbol("view-context-overlay");
  // Retain mount order when an older overlay updates or temporarily clears.
  overlays.set(key, null);
  $effect(() => {
    overlays.set(key, read() ?? null);
    schedule();
  });
  onDestroy(() => {
    overlays.delete(key);
    schedule();
  });
}

/**
 * Answer the host when it asks (after `ready`, after a frame load) and publish
 * again after a refresh it signals. Returns the teardown, which also tells the
 * host the app has nothing to describe any more.
 */
export function initViewContext(): () => void {
  const answer = () => flush({ force: true });
  const stopRequests = onViewContextRequest(answer);
  const stopRefresh = onHostRefresh(answer);
  return () => {
    stopRequests();
    stopRefresh();
    flush({ cleared: true });
  };
}
