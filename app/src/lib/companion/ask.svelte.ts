/**
 * Lets a screen hand the Companion a question about what it shows.
 *
 * The Companion cannot see the page (it runs in the SDK's iframe), so a
 * failure that never reached the backend (a deploy-time 502, a timeout) is
 * invisible to its tools. A screen that shows such a failure asks on the
 * user's behalf with the exact error, through the controller CompanionHost
 * mounted once for the app.
 */
import type { CompanionController } from "@kombiverselabs/embed";

// Raw, not a deep proxy: the unregister below compares against the controller
// it was handed, and a proxy would never equal it. A Companion mounted for a
// moment before the embedded mode was known then stayed "available" for good.
let active = $state.raw<CompanionController | null>(null);

/** Registers the mounted controller; the returned function unregisters it. */
export function registerCompanion(controller: CompanionController): () => void {
  active = controller;
  return () => {
    if (active === controller) active = null;
  };
}

export const companion = {
  get available() {
    return active !== null;
  },
  /** Opens the panel and sends `prompt` as the user's message in a new conversation. */
  ask(prompt: string): boolean {
    if (!active || !prompt.trim()) return false;
    active.submit({ prompt, freshSession: true });
    return true;
  },
};
