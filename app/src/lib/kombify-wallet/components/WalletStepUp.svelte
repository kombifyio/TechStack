<script lang="ts">
  /**
   * The Gateway asked for a fresh second factor before it releases a Wallet
   * value (`wallet_step_up_required`). This step sends the owner through
   * Techstack's Auth0 sign-in with the multi-factor policy; the Wallet page
   * reopens the same action on return, so it runs again with the new token.
   */
  import { tr as t } from "#lib/i18n.svelte.js";
  import { startGatewayStepUp } from "#lib/auth/gateway-auth.js";
  import {
    beginStepUp,
    stepUpMinutes,
    type WalletResume,
  } from "../step-up.js";

  interface Props {
    resume: WalletResume;
    /** The Gateway's freshness window, when it named one. */
    maxAgeSeconds?: number | null;
    /** Called right before the sign-in starts (keeps a sign request for the return). */
    beforeStart?: () => void;
  }

  let { resume, maxAgeSeconds = null, beforeStart }: Props = $props();

  let starting = $state(false);
  let unavailable = $state(false);
  const minutes = $derived(stepUpMinutes(maxAgeSeconds));

  async function confirm() {
    if (starting) return;
    starting = true;
    unavailable = false;
    beforeStart?.();
    try {
      const started = await beginStepUp(
        resume,
        (returnTo) => startGatewayStepUp({ returnTo }),
        window.location,
      );
      if (!started) unavailable = true;
    } catch {
      unavailable = true;
    } finally {
      starting = false;
    }
  }
</script>

<div class="wallet-step-up" data-testid="wallet-step-up">
  <p data-kx="status" data-status="warn" class="text-sm" role="status">
    {minutes === null
      ? t("kwallet.step_up.body")
      : t("kwallet.step_up.body_minutes", { minutes: String(minutes) })}
  </p>
  {#if unavailable}
    <p role="alert" class="text-destructive text-sm">
      {t("kwallet.step_up.unavailable")}
    </p>
  {/if}
  <div class="wallet-step-up__actions">
    <button
      type="button"
      class="kw-btn"
      data-kx="control"
      data-variant="primary"
      disabled={starting}
      onclick={confirm}
    >
      {starting ? t("kwallet.step_up.working") : t("kwallet.step_up.confirm")}
    </button>
  </div>
</div>

<style>
  .wallet-step-up {
    display: grid;
    gap: 0.6rem;
  }
  .wallet-step-up__actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
  }
</style>
