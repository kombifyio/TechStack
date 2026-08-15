import { redirect } from "@sveltejs/kit";
import { browser } from "$app/environment";
import type { PageLoad } from "./$types";

/**
 * Root page load function.
 * Redirects authenticated users to /stacks, unauthenticated to /login.
 * This runs before the page renders, preventing flash of content.
 */
export const load: PageLoad = async ({ url }) => {
  if (browser) {
    const { initAuth, isAuthenticated } =
      await import("$lib/stores/auth.svelte");
    await initAuth();
    if (isAuthenticated()) {
      const embedded = url.searchParams.get("embedded") === "true";
      throw redirect(302, embedded ? "/stacks?embedded=true" : "/stacks");
    } else {
      const embedded = url.searchParams.get("embedded") === "true";
      throw redirect(302, embedded ? "/login?embedded=true" : "/login");
    }
  }
  // SSR: Fall through and let client-side handle the redirect
  return {};
};
