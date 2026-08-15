<script lang="ts">
  import { authHandler } from "$lib/stores/authHandler.svelte";
  import { authStore } from "$lib/stores/auth.svelte";
  import { currentAuthReturnTo } from "$lib/auth/login-experience";
  import { tr } from "$lib/i18n.svelte";

  interface Props {
    /** Re-runs the failed loader in place. */
    onretry: () => void | Promise<void>;
    /** Disables the retry button while the loader runs. */
    busy?: boolean;
    /** Post-login return path; defaults to the current location. */
    returnTo?: string;
    /** Message id for the body copy; defaults to the renewal explanation. */
    bodyKey?: string;
  }

  const {
    onretry,
    busy = false,
    returnTo,
    bodyKey = "auth.session.renewal.body",
  }: Props = $props();

  function signInAgain() {
    authStore.initiateCloudLogin({
      returnTo: returnTo ?? currentAuthReturnTo(),
    });
  }
</script>

<!-- Self-hides while the global renewal banner is visible so prompts never stack. -->
{#if !authHandler.showReloginModal}
  <div
    class="rounded-lg border border-warning/30 bg-warning/10 p-4 text-sm text-foreground"
    data-testid="session-renewal-panel"
  >
    <p class="font-medium">{tr("auth.session.renewal.title")}</p>
    <p class="mt-1 text-muted-foreground">
      {tr(bodyKey)}
    </p>
    <div class="mt-3 flex flex-wrap gap-2">
      <button
        type="button"
        class="btn btn-primary"
        data-testid="session-renewal-signin"
        onclick={signInAgain}
      >
        {tr("auth.session.renewal.signIn")}
      </button>
      <button
        type="button"
        class="btn btn-secondary"
        data-testid="session-renewal-retry"
        onclick={() => onretry()}
        disabled={busy}
      >
        {busy
          ? tr("auth.session.renewal.retrying")
          : tr("auth.session.renewal.retry")}
      </button>
    </div>
  </div>
{/if}
