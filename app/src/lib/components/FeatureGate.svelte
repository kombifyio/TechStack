<script lang="ts">
  import {
    features,
    getDefaultEnabled,
    type FeatureFlag,
  } from "$lib/stores/features";
  import type { Snippet } from "svelte";

  interface Props {
    /** Feature key to gate on (e.g., "network_discovery", "simulation") */
    feature: string;
    /** Show a disabled/greyed out hint when feature is off */
    showDisabledHint?: boolean;
    /** Custom message when feature is disabled */
    disabledMessage?: string;
    /** Content to show when feature is enabled */
    children: Snippet;
    /** Optional fallback content when disabled (alternative to hint) */
    fallback?: Snippet;
  }

  let {
    feature,
    showDisabledHint = true,
    disabledMessage,
    children,
    fallback,
  }: Props = $props();

  // Subscribe to the allFeatures and ready derived stores
  const allFeatures = features.allFeatures;
  const ready = features.ready;

  // Get feature state from store
  const featureState = $derived($allFeatures.get(feature));

  /**
   * Determine if feature is enabled.
   * - When features are NOT yet loaded (ready=false), use the default value
   *   to avoid false-disabled flicker (UX features should appear ON immediately).
   * - When features ARE loaded, use the actual state from the backend.
   */
  const isEnabled = $derived(
    $ready
      ? (featureState?.enabled ?? getDefaultEnabled(feature))
      : getDefaultEnabled(feature),
  );
  const isLocked = $derived(featureState?.locked ?? false);
  const requiresConsent = $derived(featureState?.requires_consent ?? false);
  const hasConsent = $derived(featureState?.has_consent ?? false);
  const riskLevel = $derived(featureState?.risk_level ?? "low");

  function getDisabledMessage(): string {
    if (disabledMessage) return disabledMessage;
    if (isLocked) return "This feature requires admin privileges";
    if (requiresConsent && !hasConsent)
      return "Grant consent in Settings → Features to enable";
    return "Enable this feature in Settings → Features";
  }

  function getRiskBadgeColor(): string {
    switch (riskLevel) {
      case "high":
        return "bg-red-500/20 text-red-400 border-red-500/30";
      case "medium":
        return "bg-yellow-500/20 text-yellow-400 border-yellow-500/30";
      default:
        return "bg-green-500/20 text-green-400 border-green-500/30";
    }
  }
</script>

{#if isEnabled}
  {@render children()}
{:else if fallback}
  {@render fallback()}
{:else if showDisabledHint}
  <!-- Disabled hint with tooltip -->
  <div
    class="group relative inline-block"
    role="button"
    tabindex="0"
    aria-disabled="true"
  >
    <!-- Greyed out content wrapper -->
    <div class="opacity-50 pointer-events-none select-none grayscale">
      {@render children()}
    </div>

    <!-- Overlay tooltip on hover -->
    <div
      class="absolute bottom-full left-1/2 -translate-x-1/2 mb-2 px-3 py-2
             bg-popover border border-border rounded-lg shadow-xl
             opacity-0 group-hover:opacity-100 group-focus:opacity-100
             transition-opacity duration-200 pointer-events-none
             whitespace-nowrap z-50 text-sm"
    >
      <div class="flex items-center gap-2">
        {#if isLocked}
          <svg
            class="w-4 h-4 text-yellow-400"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z"
            />
          </svg>
        {:else if requiresConsent && !hasConsent}
          <svg
            class="w-4 h-4 text-primary"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z"
            />
          </svg>
        {:else}
          <svg
            class="w-4 h-4 text-gray-400"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z"
            />
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M15 12a3 3 0 11-6 0 3 3 0 016 0z"
            />
          </svg>
        {/if}
        <span class="text-popover-foreground">{getDisabledMessage()}</span>
      </div>

      <!-- Risk level badge if high -->
      {#if riskLevel === "high" && requiresConsent}
        <div class="mt-1.5 flex items-center gap-1">
          <span
            class="text-xs px-1.5 py-0.5 rounded border {getRiskBadgeColor()}"
          >
            High Risk
          </span>
          <span class="text-xs text-muted-foreground">Consent required</span>
        </div>
      {/if}

      <!-- Arrow pointer -->
      <div
        class="absolute top-full left-1/2 -translate-x-1/2 -mt-px
               border-8 border-transparent border-t-popover"
      ></div>
    </div>
  </div>
{/if}
