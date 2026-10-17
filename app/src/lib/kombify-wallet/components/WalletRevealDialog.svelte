<script lang="ts">
  /**
   * Reveal one Wallet item to this browser. The value is decrypted here with
   * an ephemeral key, shown masked until the owner asks to see it, and wiped
   * after 30 seconds or when the dialog closes. It is never stored, logged or
   * rendered while masked.
   */
  import { onDestroy, onMount } from "svelte";
  import Dialog from "./WalletDialog.svelte";
  import { tr as t } from "#lib/i18n.svelte.js";
  import { revealedParts, type RevealedPart } from "../item-types.js";
  import {
    isStepUpRequired,
    revealItem,
    type WalletItem,
  } from "../wallet-api.js";
  import WalletStepUp from "./WalletStepUp.svelte";
  import { fieldLabel, walletErrorMessage } from "../wallet-text.js";

  /** How long a revealed value stays in memory. */
  const REVEAL_SECONDS = 30;

  interface Props {
    item: WalletItem;
    ownerSubject: string;
    onclose: () => void;
  }

  let { item, ownerSubject, onclose }: Props = $props();

  let phase = $state<"loading" | "shown" | "cleared" | "error" | "stepup">(
    "loading",
  );
  let stepUpAge = $state<number | null>(null);
  let parts = $state<RevealedPart[] | null>(null);
  let version = $state("");
  let visible = $state(false);
  let remaining = $state(REVEAL_SECONDS);
  let copied = $state<string | null>(null);
  let copyFailed = $state(false);
  let failure = $state<string | null>(null);
  let timer: ReturnType<typeof setInterval> | undefined;
  let copiedTimer: ReturnType<typeof setTimeout> | undefined;
  let attempt = 0;
  let closed = false;

  function stopTimers() {
    if (timer !== undefined) clearInterval(timer);
    if (copiedTimer !== undefined) clearTimeout(copiedTimer);
    timer = undefined;
    copiedTimer = undefined;
  }

  /** Drop the value from component state. */
  function wipe() {
    stopTimers();
    if (parts) for (const part of parts) part.value = "";
    parts = null;
    visible = false;
    copied = null;
  }

  async function reveal() {
    const current = ++attempt;
    wipe();
    phase = "loading";
    failure = null;
    copyFailed = false;
    try {
      const result = await revealItem(item.itemId, ownerSubject);
      if (closed || current !== attempt) return;
      parts = revealedParts(item.itemType, result.value);
      version = result.version;
      phase = "shown";
      remaining = REVEAL_SECONDS;
      timer = setInterval(() => {
        remaining -= 1;
        if (remaining <= 0) {
          wipe();
          phase = "cleared";
        }
      }, 1000);
    } catch (error) {
      if (closed || current !== attempt) return;
      if (isStepUpRequired(error)) {
        stepUpAge = error.stepUpMaxAgeSeconds;
        phase = "stepup";
        return;
      }
      failure = walletErrorMessage(error);
      phase = "error";
    }
  }

  async function copy(part: RevealedPart) {
    copyFailed = false;
    try {
      await navigator.clipboard.writeText(part.value);
      copied = part.field;
      if (copiedTimer !== undefined) clearTimeout(copiedTimer);
      copiedTimer = setTimeout(() => (copied = null), 2000);
    } catch {
      copyFailed = true;
    }
  }

  function close() {
    closed = true;
    wipe();
    onclose();
  }

  onMount(() => {
    void reveal();
  });
  onDestroy(() => {
    closed = true;
    wipe();
  });
</script>

<Dialog
  open={true}
  title={t("kwallet.reveal.title", { name: item.displayName })}
  description={t("kwallet.reveal.description")}
  onclose={close}
  closeLabel={t("kwallet.close")}
>
  <div class="wallet-reveal" data-sentry-block>
    {#if phase === "loading"}
      <p role="status" class="text-muted-foreground text-sm">
        {t("kwallet.reveal.loading")}
      </p>
    {:else if phase === "stepup"}
      <WalletStepUp
        resume={{ action: "reveal", itemId: item.itemId }}
        maxAgeSeconds={stepUpAge}
      />
    {:else if phase === "error"}
      <p
        role="alert"
        data-kx="status"
        data-status="error"
        class="text-destructive text-sm"
      >
        {failure}
      </p>
      <div>
        <button
          type="button"
          class="kw-btn"
          data-kx="control"
          onclick={() => reveal()}
        >
          {t("kwallet.retry")}
        </button>
      </div>
    {:else if phase === "cleared"}
      <p role="status" class="text-muted-foreground text-sm">
        {t("kwallet.reveal.cleared")}
      </p>
      <div>
        <button
          type="button"
          class="kw-btn"
          data-kx="control"
          onclick={() => reveal()}
        >
          {t("kwallet.reveal.again")}
        </button>
      </div>
    {:else if parts}
      <div class="wallet-reveal__meta text-muted-foreground text-xs">
        <span>{t("kwallet.reveal.version", { version })}</span>
        <span>
          {t("kwallet.reveal.countdown", {
            seconds: String(remaining),
          })}
        </span>
      </div>
      <dl class="wallet-reveal__parts">
        {#each parts as part (part.field)}
          <div class="wallet-reveal__part">
            <dt class="text-muted-foreground text-xs">
              {fieldLabel(part.field)}
            </dt>
            <dd>
              {#if visible}
                <code
                  class="wallet-reveal__value"
                  data-testid="wallet-revealed-value">{part.value}</code
                >
              {:else}
                <span class="wallet-reveal__mask"
                  ><span aria-hidden="true">••••••••••••</span><span
                    class="sr-only">{t("kwallet.reveal.hidden")}</span
                  ></span
                >
              {/if}
              <button
                type="button"
                class="kw-btn"
                data-kx="control"
                aria-label={t("kwallet.reveal.copy_named", {
                  field: fieldLabel(part.field),
                })}
                onclick={() => copy(part)}
              >
                {copied === part.field
                  ? t("kwallet.reveal.copied")
                  : t("kwallet.reveal.copy")}
              </button>
            </dd>
          </div>
        {/each}
      </dl>
      <div class="wallet-reveal__actions">
        <button
          type="button"
          class="kw-btn"
          data-kx="control"
          onclick={() => (visible = !visible)}
        >
          {visible ? t("kwallet.reveal.hide") : t("kwallet.reveal.show")}
        </button>
      </div>
      {#if copyFailed}
        <p role="alert" class="text-destructive text-sm">
          {t("kwallet.reveal.copy_failed")}
        </p>
      {/if}
    {/if}
    <div class="wallet-reveal__close">
      <button
        type="button"
        class="kw-btn"
        data-kx="control"
        data-variant="primary"
        onclick={close}
      >
        {t("kwallet.close")}
      </button>
    </div>
  </div>
</Dialog>

<style>
  .wallet-reveal {
    display: grid;
    gap: 0.85rem;
  }
  .wallet-reveal__meta {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
  }
  .wallet-reveal__parts {
    display: grid;
    gap: 0.6rem;
    margin: 0;
  }
  .wallet-reveal__part dd {
    display: flex;
    align-items: flex-start;
    gap: 0.5rem;
    margin: 0.2rem 0 0;
  }
  .wallet-reveal__value,
  .wallet-reveal__mask {
    flex: 1;
    min-width: 0;
    padding: 0.4rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-md, 0.5rem);
    font-family: var(--font-mono, ui-monospace, monospace);
    font-size: 0.85rem;
  }
  .wallet-reveal__value {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    max-height: 16rem;
    overflow: auto;
  }
  .wallet-reveal__mask {
    letter-spacing: 0.1em;
    color: var(--muted-foreground);
  }
  .wallet-reveal__actions,
  .wallet-reveal__close {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
  }
</style>
