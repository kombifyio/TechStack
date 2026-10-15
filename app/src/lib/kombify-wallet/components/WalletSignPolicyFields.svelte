<script lang="ts">
  /**
   * What an SSH certificate authority may sign (`signPolicy`): the kinds of
   * certificate, the principals, the longest lifetimes and the user
   * certificate extensions. Every certificate the Wallet signs stays inside
   * this bound; the store checks it again at each signature.
   */
  import { tr as t } from "#lib/i18n.svelte.js";
  import {
    MAX_POLICY_PRINCIPALS,
    USER_EXTENSIONS,
    type SignPolicyDraft,
    type SignPolicyErrors,
    type UserExtension,
  } from "../ssh-ca.js";

  interface Props {
    draft: SignPolicyDraft;
    errors: SignPolicyErrors;
    idPrefix: string;
    disabled?: boolean;
  }

  let {
    draft = $bindable(),
    errors,
    idPrefix,
    disabled = false,
  }: Props = $props();

  const id = (field: string) => `${idPrefix}-policy-${field}`;

  function toggleExtension(name: UserExtension, on: boolean) {
    draft.extensions = USER_EXTENSIONS.filter((known) =>
      known === name ? on : draft.extensions.includes(known),
    );
  }
</script>

<fieldset class="policy" {disabled}>
  <legend class="policy__legend">{t("kwallet.ssh_ca.policy.legend")}</legend>
  <p class="text-muted-foreground text-xs">
    {t("kwallet.ssh_ca.policy.description")}
  </p>

  <div role="group" aria-labelledby={id("kinds-label")} class="policy__kinds">
    <span id={id("kinds-label")} class="policy__label">
      {t("kwallet.ssh_ca.policy.kinds")}
    </span>
    <label class="policy__option">
      <input
        id={id("user")}
        type="checkbox"
        checked={draft.user}
        aria-invalid={errors.kinds ? "true" : undefined}
        aria-describedby={`${id("user-hint")}${errors.kinds ? ` ${id("kinds-error")}` : ""}`}
        onchange={(event) => (draft.user = event.currentTarget.checked)}
      />
      <span>
        <span class="policy__name">{t("kwallet.ssh_ca.policy.kind_user")}</span>
        <span id={id("user-hint")} class="text-muted-foreground block text-xs">
          {t("kwallet.ssh_ca.policy.kind_user_hint")}
        </span>
      </span>
    </label>
    {#if draft.user}
      <div class="policy__nested">
        <label class="policy__label" for={id("maxUser")}>
          {t("kwallet.ssh_ca.policy.max_user")}
        </label>
        <input
          id={id("maxUser")}
          class="kw-input policy__duration"
          type="text"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          aria-invalid={errors.maxUser ? "true" : undefined}
          aria-describedby={`${id("duration-hint")}${errors.maxUser ? ` ${id("maxUser-error")}` : ""}`}
          bind:value={draft.maxUser}
        />
        {#if errors.maxUser}
          <p id={id("maxUser-error")} class="text-destructive mt-1 text-xs">
            {t("kwallet.ssh_ca.error.max_user_invalid")}
          </p>
        {/if}
      </div>
    {/if}

    <label class="policy__option">
      <input
        id={id("host")}
        type="checkbox"
        checked={draft.host}
        aria-invalid={errors.kinds ? "true" : undefined}
        aria-describedby={`${id("host-hint")}${errors.kinds ? ` ${id("kinds-error")}` : ""}`}
        onchange={(event) => (draft.host = event.currentTarget.checked)}
      />
      <span>
        <span class="policy__name">{t("kwallet.ssh_ca.policy.kind_host")}</span>
        <span id={id("host-hint")} class="text-muted-foreground block text-xs">
          {t("kwallet.ssh_ca.policy.kind_host_hint")}
        </span>
      </span>
    </label>
    {#if draft.host}
      <div class="policy__nested">
        <label class="policy__label" for={id("maxHost")}>
          {t("kwallet.ssh_ca.policy.max_host")}
        </label>
        <input
          id={id("maxHost")}
          class="kw-input policy__duration"
          type="text"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          aria-invalid={errors.maxHost ? "true" : undefined}
          aria-describedby={`${id("duration-hint")}${errors.maxHost ? ` ${id("maxHost-error")}` : ""}`}
          bind:value={draft.maxHost}
        />
        {#if errors.maxHost}
          <p id={id("maxHost-error")} class="text-destructive mt-1 text-xs">
            {t("kwallet.ssh_ca.error.max_host_invalid")}
          </p>
        {/if}
      </div>
    {/if}
    {#if draft.user || draft.host}
      <p id={id("duration-hint")} class="text-muted-foreground text-xs">
        {t("kwallet.ssh_ca.policy.duration_hint")}
      </p>
    {/if}
    {#if errors.kinds}
      <p id={id("kinds-error")} class="text-destructive text-xs">
        {t("kwallet.ssh_ca.error.kinds_required")}
      </p>
    {/if}
  </div>

  <div>
    <label class="policy__label" for={id("principals")}>
      {t("kwallet.ssh_ca.policy.principals")}
    </label>
    <textarea
      id={id("principals")}
      class="kw-input policy__mono"
      rows="3"
      autocomplete="off"
      autocapitalize="off"
      spellcheck="false"
      required
      aria-invalid={errors.principals ? "true" : undefined}
      aria-describedby={`${id("principals-hint")}${errors.principals ? ` ${id("principals-error")}` : ""}`}
      bind:value={draft.principals}
    ></textarea>
    <p id={id("principals-hint")} class="text-muted-foreground mt-1 text-xs">
      {t("kwallet.ssh_ca.policy.principals_hint")}
    </p>
    {#if errors.principals}
      <p id={id("principals-error")} class="text-destructive mt-1 text-xs">
        {#if errors.principals === "required"}
          {t("kwallet.ssh_ca.error.principals_required")}
        {:else if errors.principals === "too_many"}
          {t("kwallet.ssh_ca.error.principals_too_many", {
            max: String(MAX_POLICY_PRINCIPALS),
          })}
        {:else}
          {t("kwallet.ssh_ca.error.principals_invalid", {
            principal: errors.principal ?? "",
          })}
        {/if}
      </p>
    {/if}
  </div>

  {#if draft.user}
    <div role="group" aria-labelledby={id("extensions-label")}>
      <span id={id("extensions-label")} class="policy__label">
        {t("kwallet.ssh_ca.policy.extensions")}
      </span>
      <div class="policy__extensions">
        {#each USER_EXTENSIONS as name (name)}
          <label class="policy__extension">
            <input
              type="checkbox"
              checked={draft.extensions.includes(name)}
              onchange={(event) =>
                toggleExtension(name, event.currentTarget.checked)}
            />
            <code>{name}</code>
          </label>
        {/each}
      </div>
      <p class="text-muted-foreground mt-1 text-xs">
        {t("kwallet.ssh_ca.policy.extensions_hint")}
      </p>
    </div>

    <label class="policy__option">
      <input
        type="checkbox"
        checked={draft.requireSourceAddress}
        aria-describedby={id("source-hint")}
        onchange={(event) =>
          (draft.requireSourceAddress = event.currentTarget.checked)}
      />
      <span>
        <span class="policy__name">
          {t("kwallet.ssh_ca.policy.require_source")}
        </span>
        <span
          id={id("source-hint")}
          class="text-muted-foreground block text-xs"
        >
          {t("kwallet.ssh_ca.policy.require_source_hint")}
        </span>
      </span>
    </label>
  {/if}
</fieldset>

<style>
  .policy {
    display: grid;
    gap: 0.85rem;
    margin: 0;
    padding: 0;
    border: 0;
    min-width: 0;
  }
  .policy__legend {
    margin-bottom: 0.25rem;
    font-size: 0.85rem;
    font-weight: 500;
    color: var(--muted-foreground);
  }
  .policy__label {
    display: block;
    margin-bottom: 0.25rem;
    font-size: 0.85rem;
    font-weight: 500;
    color: var(--muted-foreground);
  }
  .policy__kinds {
    display: grid;
    gap: 0.35rem;
  }
  .policy__option {
    display: flex;
    align-items: flex-start;
    gap: 0.6rem;
    padding: 0.5rem 0.65rem;
    border-radius: var(--radius-md, 0.5rem);
    border: 1px solid transparent;
    cursor: pointer;
  }
  .policy__option input {
    margin-top: 0.2rem;
  }
  .policy__option:focus-within {
    outline: 2px solid var(--ring, var(--primary));
    outline-offset: 1px;
  }
  .policy__name {
    display: block;
    font-size: 0.9rem;
    font-weight: 500;
  }
  .policy__nested {
    margin-inline-start: 2rem;
  }
  .policy__duration {
    width: 10rem;
    max-width: 100%;
  }
  .policy__mono {
    font-family: var(--font-mono, ui-monospace, monospace);
    font-size: 0.85rem;
  }
  .policy__extensions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.35rem 1rem;
  }
  .policy__extension {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.85rem;
  }
</style>
