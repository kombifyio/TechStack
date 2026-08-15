import { redirect } from "@sveltejs/kit";
import { browser } from "$app/environment";
import type { LayoutLoad } from "./$types";

/**
 * Protected route load function.
 * Checks authentication status and redirects to login if not authenticated.
 *
 * Note: Since PocketBase auth is stored client-side (localStorage),
 * we can only check auth in the browser. Server-side requests will
 * pass through and rely on client-side hydration to redirect.
 */
export const load: LayoutLoad = async () => {
  if (browser) {
    const { initAuth, isAuthenticated } =
      await import("$lib/stores/auth.svelte");
    await initAuth();
    if (!isAuthenticated()) {
      const { refreshEmbeddedCloudSession } =
        await import("$lib/auth/embedded-session");
      if (await refreshEmbeddedCloudSession()) {
        return {};
      }
      const { loginRedirectForWindowsLocalClient } =
        await import("$lib/client/windows-onboarding");
      throw redirect(
        302,
        loginRedirectForWindowsLocalClient(window.localStorage),
      );
    }
  }
  return {};
};
