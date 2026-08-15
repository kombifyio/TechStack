import { redirect } from "@sveltejs/kit";
import { browser } from "$app/environment";
import type { LayoutLoad } from "./$types";

/**
 * Protected route load function.
 * Checks authentication status and redirects to login if not authenticated.
 */
export const load: LayoutLoad = async () => {
  if (browser) {
    const { initAuth, isAuthenticated } =
      await import("$lib/stores/auth.svelte");
    await initAuth();
    if (!isAuthenticated()) {
      throw redirect(302, "/login");
    }
  }
  return {};
};
