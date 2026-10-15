<script lang="ts">
  /**
   * The public key of one SSH certificate authority, with a copy button. It is
   * not secret: servers trust it to accept the certificates the authority
   * signs.
   */
  import { onDestroy } from "svelte";
  import { tr as t } from "#lib/i18n.svelte.js";
  import { copyText } from "../browser-io.js";

  interface Props {
    publicKey: string;
    name: string;
  }

  let { publicKey, name }: Props = $props();

  let copied = $state(false);
  let copyFailed = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  async function copy() {
    copyFailed = false;
    if (!(await copyText(publicKey))) {
      copyFailed = true;
      return;
    }
    copied = true;
    if (timer !== undefined) clearTimeout(timer);
    timer = setTimeout(() => (copied = false), 2000);
  }

  onDestroy(() => {
    if (timer !== undefined) clearTimeout(timer);
  });
</script>

<div class="ca-key" data-testid="wallet-ca-key">
  <span class="text-muted-foreground block text-xs">
    {t("kwallet.ssh_ca.public_key")}
  </span>
  <div class="ca-key__line">
    <code class="ca-key__value" data-testid="wallet-ca-public-key"
      >{publicKey}</code
    >
    <button
      type="button"
      class="kw-btn"
      data-kx="control"
      aria-label={t("kwallet.ssh_ca.copy_key_named", { name })}
      onclick={copy}
    >
      {copied ? t("kwallet.ssh_ca.copied") : t("kwallet.ssh_ca.copy")}
    </button>
  </div>
  <span class="text-muted-foreground block text-xs">
    {t("kwallet.ssh_ca.public_key_hint")}
  </span>
  {#if copyFailed}
    <span role="alert" class="text-destructive block text-xs">
      {t("kwallet.ssh_ca.copy_failed")}
    </span>
  {/if}
</div>

<style>
  .ca-key {
    display: grid;
    gap: 0.2rem;
    margin-top: 0.4rem;
    max-width: 38rem;
  }
  .ca-key__line {
    display: flex;
    align-items: flex-start;
    gap: 0.5rem;
  }
  .ca-key__value {
    flex: 1;
    min-width: 0;
    padding: 0.3rem 0.5rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-md, 0.5rem);
    font-family: var(--font-mono, ui-monospace, monospace);
    font-size: 0.75rem;
    font-weight: 400;
    overflow-wrap: anywhere;
    user-select: all;
  }
</style>
