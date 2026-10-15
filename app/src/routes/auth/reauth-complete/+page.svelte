<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  /**
   * Step-up Complete Page
   *
   * Landing page after a fresh sign-in that an embedded frame requested in a
   * new window (terminal access, SSH key authorization). It tells the waiting
   * frame the sign-in is done and closes itself; when the browser refuses to
   * close it, the page says the owner can.
   */

  import { onMount } from "svelte";
  import { browser } from "$app/env";
  import { announceStepUpComplete } from "#lib/auth/step-up.js";
  import { CheckCircle } from "@lucide/svelte";

  onMount(() => {
    if (!browser) return;
    announceStepUpComplete();
    const timer = window.setTimeout(() => window.close(), 700);
    return () => window.clearTimeout(timer);
  });
</script>

<svelte:head>
  <title>{tr("ui.stepUp.doneTitle")}</title>
</svelte:head>

<div class="min-h-screen bg-background flex items-center justify-center">
  <div class="max-w-md w-full px-6 text-center" data-testid="step-up-complete">
    <div class="mb-6">
      <CheckCircle class="h-12 w-12 mx-auto text-success" />
    </div>
    <h1 class="text-2xl font-bold text-foreground mb-2">
      {tr("ui.stepUp.doneTitle")}
    </h1>
    <p class="text-muted-foreground">{tr("ui.stepUp.doneBody")}</p>
  </div>
</div>
