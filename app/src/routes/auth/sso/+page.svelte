<script lang="ts">
  /**
   * SSO Landing Page
   *
   * This page handles SSO redirects from kombify Cloud Portal.
   * Embedded portal users request a short-lived SSO JWT from the parent frame.
   * Legacy top-level redirects may still provide a token in the URL fragment.
   *
   * Flow:
   * 1. Portal loads: /auth/sso?embedded=true&return_url=/dashboard
   * 2. This page requests the token via postMessage, or reads legacy fragments
   * 3. Sends token to /api/v1/auth/portal-verify
   * 4. On success, the backend establishes techstack_session and this page
   *    redirects to return_url
   */

  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { browser } from "$app/env";
  import { refreshEmbeddedCloudSession } from "#lib/auth/embedded-session.js";
  import {
    buildSsoContinuationPath,
    sanitizeAuthReturnTo,
  } from "#lib/auth/login-experience.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import {
    hostNavigationRequested,
    withHostNavigation,
  } from "#lib/embedded-navigation.js";
  import { Loader2, CheckCircle, XCircle } from "@lucide/svelte";

  // State
  let status = $state<"loading" | "success" | "error">("loading");
  let errorMessage = $state<string | null>(null);
  let returnUrl = $state("/dashboard");
  let isEmbedded = $state(false);
  let hostNavigation = $state(false);

  onMount(() => {
    if (!browser) return;

    // Detect embedded mode
    const queryParams = new URLSearchParams(window.location.search);
    isEmbedded =
      queryParams.get("embedded") === "true" || window.self !== window.top;
    hostNavigation = isEmbedded && hostNavigationRequested(queryParams);

    void authenticate();
  });

  async function authenticate(): Promise<void> {
    status = "loading";
    errorMessage = null;
    const queryParams = new URLSearchParams(window.location.search);

    try {
      // Extract token from URL fragment
      const hash = window.location.hash.substring(1);
      const params = new URLSearchParams(hash);
      const token = params.get("token");
      const returnPath =
        params.get("return_url") ?? queryParams.get("return_url");

      const target = sanitizeAuthReturnTo(returnPath ?? "/dashboard");
      returnUrl = target;

      if (isEmbedded) {
        const refreshed = await refreshEmbeddedCloudSession();
        if (!refreshed) {
          throw new Error(
            "Your kombify Cloud session could not be restored. Please try again.",
          );
        }
        await finishAuthentication();
        return;
      }

      if (!token) {
        // No token - check query params (alternative format)
        const queryToken = queryParams.get("token");

        if (!queryToken) {
          throw new Error("No SSO token provided");
        }

        await completeLegacyLogin(queryToken);
      } else {
        await completeLegacyLogin(token);
      }
    } catch (err) {
      console.error("[SSO] Error:", err);
      status = "error";
      errorMessage =
        err instanceof Error ? err.message : "SSO authentication failed";
    }
  }

  async function completeLegacyLogin(token: string): Promise<void> {
    await authStore.completePortalLogin(token);
    await finishAuthentication();
  }

  async function finishAuthentication(): Promise<void> {
    // Success!
    status = "success";

    // Clean the token/fragment while retaining the continuation. The root
    // layout can remount this page when it switches to the authenticated,
    // host-owned branch during the delay below.
    window.history.replaceState(
      {},
      "",
      buildSsoContinuationPath({
        returnTo: returnUrl,
        embedded: isEmbedded,
        hostNavigation,
      }),
    );

    // Short delay for UX, then redirect
    await new Promise((resolve) => setTimeout(resolve, 500));
    // SvelteKit 3: goto() rejects destinations that do not resolve to an
    // application route, so a caller-supplied return_url can no longer strand
    // the user on this page. Fall back to the default landing route.
    try {
      await goto(hostNavigation ? withHostNavigation(returnUrl) : returnUrl);
    } catch {
      await goto(
        hostNavigation ? withHostNavigation("/dashboard") : "/dashboard",
      );
    }
  }

  function handleRetry(): void {
    if (isEmbedded) {
      void authenticate();
      return;
    }
    window.location.href = "/login";
  }
</script>

<svelte:head>
  <title>SSO Authentication - kombify-Techstack</title>
</svelte:head>

<div class="min-h-screen bg-background flex items-center justify-center">
  <div class="max-w-md w-full px-6">
    {#if status === "loading"}
      <!-- Loading State -->
      <div class="text-center">
        <div class="mb-6">
          <Loader2 class="h-12 w-12 mx-auto text-primary animate-spin" />
        </div>
        <h1 class="text-2xl font-bold text-foreground mb-2">
          Signing you in...
        </h1>
        <p class="text-muted-foreground">Verifying your portal credentials</p>
      </div>
    {:else if status === "success"}
      <!-- Success State -->
      <div class="text-center">
        <div class="mb-6">
          <CheckCircle class="h-12 w-12 mx-auto text-success" />
        </div>
        <h1 class="text-2xl font-bold text-foreground mb-2">Welcome back!</h1>
        <p class="text-muted-foreground">Redirecting to your dashboard...</p>
      </div>
    {:else}
      <!-- Error State -->
      <div class="text-center">
        <div class="mb-6">
          <XCircle class="h-12 w-12 mx-auto text-destructive" />
        </div>
        <h1 class="text-2xl font-bold text-foreground mb-2">
          Authentication Failed
        </h1>
        <p class="text-muted-foreground mb-6">
          {errorMessage || "Unable to verify your credentials"}
        </p>
        <div class="space-y-3">
          <button
            onclick={handleRetry}
            class="w-full py-3 px-4 bg-primary hover:bg-primary/90 text-primary-foreground rounded-lg font-medium transition-colors"
          >
            Try Again
          </button>
          {#if !isEmbedded}
            <a
              href="/"
              class="block w-full py-3 px-4 bg-secondary hover:bg-secondary/80 text-foreground rounded-lg font-medium transition-colors text-center"
            >
              Go Home
            </a>
          {/if}
        </div>
      </div>
    {/if}
  </div>
</div>
