import { redirect } from "@sveltejs/kit";
import { browser } from "$app/env";
import type { LayoutLoad } from "./$types";

/**
 * Protected route load function.
 * Checks authentication status and redirects to login if not authenticated.
 *
 * Auth state and embedded-session renewal are client-owned, so prerendered
 * requests pass through and client hydration performs the redirect.
 */
export const load: LayoutLoad = async () => {
  if (browser) {
    const { initAuth, isAuthenticated } =
      await import("#lib/stores/auth.svelte.js");
    await initAuth();
    if (!isAuthenticated()) {
      const { refreshEmbeddedCloudSession } =
        await import("#lib/auth/embedded-session.js");
      if (await refreshEmbeddedCloudSession()) {
        return {};
      }
      const { resolveUnauthenticatedEntry } =
        await import("#lib/auth/unauthenticated-redirect.js");
      throw redirect(302, resolveUnauthenticatedEntry());
    }
  }
  return {};
};
