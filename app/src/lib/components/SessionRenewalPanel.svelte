<script lang="ts">
  import { authHandler } from "#lib/stores/authHandler.svelte.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import { startGatewayLogin } from "#lib/auth/gateway-auth.js";
  import { currentAuthReturnTo } from "#lib/auth/login-experience.js";
  import { tr } from "#lib/i18n.svelte.js";
  import Button from "#lib/components/ui/Button.svelte";

  interface Props {
    /** Post-login return path; defaults to the current location. */
    returnTo?: string;
    /** Message id for the body copy; defaults to the renewal explanation. */
    bodyKey?: string;
  }

  const {
    returnTo,
    bodyKey = "auth.session.renewal.body",
  }: Props = $props();

  async function signInAgain() {
    const target = returnTo ?? currentAuthReturnTo();
    const started = await startGatewayLogin({
      interactive: true,
      returnTo: target,
    });
    if (!started) {
      authStore.initiateCloudLogin({
        returnTo: target,
        interactive: true,
      });
    }
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
      <Button
        variant="primary"
        testId="session-renewal-signin"
        onclick={signInAgain}
      >
        {tr("auth.session.renewal.signIn")}
      </Button>
    </div>
  </div>
{/if}
