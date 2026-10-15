<script lang="ts">
  /**
   * Asks the owner for the one right a refused request needs: to let Techstack
   * save credentials in their kombify Wallet (`wallet_create_right_missing`),
   * or to let it use one item (`wallet_right_missing`, right `use`). Hosted
   * Techstack only. The refused action is not replayed; the owner starts it
   * again once allowed.
   */
  import { tr } from "#lib/i18n.svelte.js";
  import Button from "#lib/components/ui/Button.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import { setServiceAccess, setServiceCreator } from "../wallet-api.js";
  import { TECHSTACK_SERVICE } from "../service-access.js";
  import { walletErrorMessage } from "../wallet-text.js";
  import { walletPrompt } from "../denials.svelte.js";
  import "./wallet.css";

  let working = $state(false);
  let allowed = $state(false);
  let failure = $state<string | null>(null);

  const request = $derived(
    authStore.walletSource === "kombify" ? walletPrompt.request : null,
  );
  const kind = $derived(request?.kind ?? "create");

  function close() {
    walletPrompt.dismiss();
    allowed = false;
    failure = null;
  }

  async function allow() {
    if (working || !request) return;
    working = true;
    failure = null;
    try {
      allowed =
        request.kind === "use"
          ? await setServiceAccess(
              request.itemId,
              TECHSTACK_SERVICE,
              "use",
              true,
            )
          : await setServiceCreator(TECHSTACK_SERVICE, true);
      if (!allowed) failure = tr("kwallet.denied.generic");
    } catch (error) {
      failure = walletErrorMessage(error);
    } finally {
      working = false;
    }
  }
</script>

{#if request}
  <Modal
    title={tr(`kwallet.prompt.${kind}.title`)}
    description={tr(
      allowed ? `kwallet.prompt.${kind}.done` : `kwallet.prompt.${kind}.body`,
    )}
    onClose={close}
    maxWidth="md"
  >
    <div class="kw-stack" data-testid="wallet-prompt" data-kind={kind}>
      {#if failure}
        <p role="alert" class="kw-notice" data-tone="error">{failure}</p>
      {/if}
      <div class="kw-actions">
        {#if allowed}
          <Button
            variant="primary"
            size="sm"
            testId="wallet-prompt-close"
            onclick={close}
          >
            {tr("kwallet.close")}
          </Button>
        {:else}
          <Button
            variant="secondary"
            size="sm"
            disabled={working}
            onclick={close}
          >
            {tr("kwallet.prompt.later")}
          </Button>
          <Button
            variant="primary"
            size="sm"
            disabled={working}
            testId="wallet-prompt-allow"
            onclick={() => void allow()}
          >
            {working
              ? tr("kwallet.working")
              : tr(`kwallet.prompt.${kind}.allow`)}
          </Button>
        {/if}
      </div>
    </div>
  </Modal>
{/if}
