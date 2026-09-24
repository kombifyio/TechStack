import { authStore } from "#lib/stores/auth.svelte.js";
import { unauthenticatedEntryPath } from "#lib/auth/login-experience.js";
import { hasWindowsLocalClientContext } from "#lib/client/windows-onboarding.js";
import { hostNavigationRequested } from "#lib/embedded-navigation.js";

export function resolveUnauthenticatedEntry(): string {
  let windowsLocal = false;
  let windowsClient = false;
  try {
    windowsLocal = hasWindowsLocalClientContext(window.localStorage);
    windowsClient =
      new URLSearchParams(window.location.search).get("client") === "windows";
  } catch {
    windowsLocal = false;
    windowsClient = false;
  }

  return unauthenticatedEntryPath({
    deploymentMode: authStore.deploymentMode,
    embedded: typeof window !== "undefined" && window.parent !== window,
    hostNavigation:
      typeof window !== "undefined" &&
      hostNavigationRequested(new URL(window.location.href)),
    windowsClient,
    windowsLocal,
  });
}
