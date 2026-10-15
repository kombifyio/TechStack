<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import Button from "#lib/components/ui/Button.svelte";
  import StepUpPrompt from "#lib/components/server-access/StepUpPrompt.svelte";
  import { isEmbeddedWindow } from "#lib/auth/embedded-session.js";
  import { FRESH_SIGN_IN, redirectToStepUp } from "#lib/auth/step-up.js";

  /**
   * A Guard change that needs a fresh sign-in. Standalone, the button sends
   * the owner through the sign-in and back to this page. Embedded in kombify
   * Cloud the frame must not navigate, so the sign-in opens in a separate
   * window; once it reports back the notice is cleared and the owner repeats
   * the change by hand. These writes are never repeated on their own.
   */
  interface Props {
    body: string;
    testId?: string;
    class?: string;
    /** The owner confirmed in the separate window; clear the notice. */
    onConfirmed: () => void;
  }

  let { body, testId, class: className = "", onConfirmed }: Props = $props();
</script>

<div
  class="rounded-md border border-info/40 bg-info/10 p-3 text-xs text-foreground {className}"
  role="alert"
  data-testid={testId}
>
  {#if isEmbeddedWindow()}
    <StepUpPrompt request={FRESH_SIGN_IN} {body} onRetry={onConfirmed} />
  {:else}
    <p>{body}</p>
    <div class="mt-2">
      <Button
        variant="primary"
        size="sm"
        onclick={() => redirectToStepUp(FRESH_SIGN_IN)}
      >
        {tr("guard.outcomes.stepUpAction")}
      </Button>
    </div>
  {/if}
</div>
