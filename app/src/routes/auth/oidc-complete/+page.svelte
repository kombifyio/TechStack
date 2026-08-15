<script lang="ts">
  /**
   * OIDC Complete Page
   *
   * Handles the redirect from the backend OIDC callback handler.
   * The backend exchanges the authorization code and redirects here
   * with the legacy compatibility auth token in the URL fragment.
   *
   * Flow:
   * 1. Backend redirects to: /auth/oidc-complete#pb_token=xxx&user={"id":"..."}
   * 2. This page reads the token from the fragment
   * 3. Stores it in local compatibility storage
   * 4. Redirects to the main dashboard
   */

  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { browser } from "$app/environment";
  import { savePocketBaseCompatStoredSession } from "$lib/auth/pocketbase-compat";

  let status = $state<"loading" | "success" | "error">("loading");
  let errorMessage = $state<string | null>(null);

  onMount(async () => {
    if (!browser) return;

    try {
      const hash = window.location.hash.substring(1);
      const params = new URLSearchParams(hash);
      const pbToken = params.get("pb_token");
      const userJSON = params.get("user");

      if (!pbToken || !userJSON) {
        throw new Error("Missing authentication data");
      }

      const user = JSON.parse(userJSON);

      savePocketBaseCompatStoredSession(pbToken, user);

      status = "success";

      // Clean URL fragment
      window.history.replaceState({}, "", "/auth/oidc-complete");

      // Short delay for UX, then redirect
      await new Promise((resolve) => setTimeout(resolve, 500));
      await goto("/stacks");
    } catch (err) {
      console.error("[OIDC] Error:", err);
      status = "error";
      errorMessage =
        err instanceof Error ? err.message : "Authentication failed";
    }
  });
</script>

<svelte:head>
  <title>Signing In - kombify-TechStack</title>
</svelte:head>

<div class="min-h-screen bg-background flex items-center justify-center">
  <div class="max-w-md w-full px-6">
    {#if status === "loading"}
      <div class="text-center">
        <div class="mb-6">
          <div
            class="animate-spin h-12 w-12 mx-auto border-4 border-primary border-t-transparent rounded-full"
          ></div>
        </div>
        <h1 class="text-2xl font-bold text-foreground mb-2">
          Signing you in...
        </h1>
        <p class="text-muted-foreground">Completing authentication</p>
      </div>
    {:else if status === "success"}
      <div class="text-center">
        <div class="mb-6">
          <div
            class="h-12 w-12 mx-auto rounded-full bg-primary/20 flex items-center justify-center"
          >
            <svg
              class="h-6 w-6 text-primary"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                d="M5 13l4 4L19 7"
              />
            </svg>
          </div>
        </div>
        <h1 class="text-2xl font-bold text-foreground mb-2">Welcome!</h1>
        <p class="text-muted-foreground">Redirecting to your dashboard...</p>
      </div>
    {:else}
      <div class="text-center">
        <div class="mb-6">
          <div
            class="h-12 w-12 mx-auto rounded-full bg-destructive/20 flex items-center justify-center"
          >
            <svg
              class="h-6 w-6 text-destructive"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                d="M6 18L18 6M6 6l12 12"
              />
            </svg>
          </div>
        </div>
        <h1 class="text-2xl font-bold text-foreground mb-2">
          Authentication Failed
        </h1>
        <p class="text-muted-foreground mb-6">
          {errorMessage || "Unable to complete sign-in"}
        </p>
        <div class="space-y-3">
          <a
            href="/login"
            class="block w-full py-3 px-4 bg-primary text-primary-foreground rounded-lg font-medium transition-colors text-center hover:bg-primary/90"
          >
            Try Again
          </a>
        </div>
      </div>
    {/if}
  </div>
</div>
