<script lang="ts">
  /**
   * Where a new SSH certificate authority's key comes from. The Wallet makes
   * it by default, so the private key never exists outside custody. An
   * existing unencrypted Ed25519 key can be imported instead; it is encrypted
   * in this browser like any other value and wiped when the dialog closes.
   */
  import { tr as t } from "#lib/i18n.svelte.js";
  import type { CaSource, ValueDraft } from "../item-types.js";

  interface Props {
    draft: ValueDraft;
    error?: string;
    idPrefix: string;
    disabled?: boolean;
  }

  let {
    draft = $bindable(),
    error,
    idPrefix,
    disabled = false,
  }: Props = $props();

  const SOURCES = [
    [
      "generate",
      "kwallet.ssh_ca.source.generate",
      "kwallet.ssh_ca.source.generate_hint",
    ],
    [
      "import",
      "kwallet.ssh_ca.source.import",
      "kwallet.ssh_ca.source.import_hint",
    ],
  ] as const;

  function choose(source: CaSource) {
    draft.privateKey = "";
    draft.caSource = source;
  }
</script>

<div class="ca-source" data-sentry-mask>
  <fieldset class="ca-source__choices" {disabled}>
    <legend class="ca-source__label">
      {t("kwallet.ssh_ca.source_label")}
    </legend>
    {#each SOURCES as [source, label, hint] (source)}
      <label
        class="ca-source__option"
        data-kx={draft.caSource === source ? "selection" : undefined}
      >
        <input
          type="radio"
          name={`${idPrefix}-ca-source`}
          value={source}
          checked={draft.caSource === source}
          aria-describedby={`${idPrefix}-ca-source-${source}-hint`}
          onchange={() => choose(source)}
        />
        <span>
          <span class="ca-source__name">{t(label)}</span>
          <span
            id={`${idPrefix}-ca-source-${source}-hint`}
            class="text-muted-foreground block text-xs"
          >
            {t(hint)}
          </span>
        </span>
      </label>
    {/each}
  </fieldset>

  {#if draft.caSource === "import"}
    <div>
      <label class="ca-source__label" for={`${idPrefix}-privateKey`}>
        {t("kwallet.ssh_ca.import_key")}
      </label>
      <textarea
        id={`${idPrefix}-privateKey`}
        class="kw-input ca-source__mono"
        rows="6"
        autocomplete="off"
        autocapitalize="off"
        spellcheck="false"
        required
        {disabled}
        aria-invalid={error ? "true" : undefined}
        aria-describedby={`${idPrefix}-privateKey-hint${error ? ` ${idPrefix}-privateKey-error` : ""}`}
        bind:value={draft.privateKey}
      ></textarea>
      <p
        id={`${idPrefix}-privateKey-hint`}
        class="text-muted-foreground mt-1 text-xs"
      >
        {t("kwallet.ssh_ca.import_key_hint")}
      </p>
      {#if error}
        <p
          id={`${idPrefix}-privateKey-error`}
          class="text-destructive mt-1 text-xs"
        >
          {error}
        </p>
      {/if}
    </div>
  {/if}
</div>

<style>
  .ca-source {
    display: grid;
    gap: 0.85rem;
  }
  fieldset {
    display: grid;
    gap: 0.35rem;
    margin: 0;
    padding: 0;
    border: 0;
    min-width: 0;
  }
  .ca-source__label {
    display: block;
    margin-bottom: 0.25rem;
    font-size: 0.85rem;
    font-weight: 500;
    color: var(--muted-foreground);
  }
  .ca-source__option {
    display: flex;
    align-items: flex-start;
    gap: 0.6rem;
    padding: 0.5rem 0.65rem;
    border-radius: var(--radius-md, 0.5rem);
    border: 1px solid transparent;
    cursor: pointer;
  }
  .ca-source__option input {
    margin-top: 0.2rem;
  }
  .ca-source__option:focus-within {
    outline: 2px solid var(--ring, var(--primary));
    outline-offset: 1px;
  }
  .ca-source__name {
    display: block;
    font-size: 0.9rem;
    font-weight: 500;
  }
  .ca-source__mono {
    font-family: var(--font-mono, ui-monospace, monospace);
    font-size: 0.85rem;
  }
</style>
