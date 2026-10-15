<script lang="ts">
  /**
   * The value fields of one Wallet item type. Values stay in this browser's
   * memory until the dialog encrypts them; inputs never autocomplete, spell
   * check or reach analytics.
   */
  import { tr } from "#lib/i18n.svelte.js";
  import type {
    EditorItemType,
    ValueDraft,
    ValueFieldName,
  } from "../item-types.js";
  import "./wallet.css";

  interface Props {
    type: EditorItemType;
    draft: ValueDraft;
    errors: Partial<Record<ValueFieldName, string>>;
    idPrefix: string;
    disabled?: boolean;
    /** Server access: host and user come from elsewhere (the wizard). */
    lockedFields?: ValueFieldName[];
    /** Server access: the sign-in method is already decided. */
    lockAuth?: boolean;
  }

  let {
    type,
    draft = $bindable(),
    errors,
    idPrefix,
    disabled = false,
    lockedFields = [],
    lockAuth = false,
  }: Props = $props();

  const id = (field: ValueFieldName) => `${idPrefix}-${field}`;
  const errorId = (field: ValueFieldName) => `${idPrefix}-${field}-error`;
  const hintId = (field: ValueFieldName) => `${idPrefix}-${field}-hint`;
  const describedBy = (field: ValueFieldName, hint = false) =>
    [hint ? hintId(field) : "", errors[field] ? errorId(field) : ""]
      .filter(Boolean)
      .join(" ") || undefined;
  const locked = (field: ValueFieldName) =>
    disabled || lockedFields.includes(field);
</script>

{#snippet fieldError(field: ValueFieldName)}
  {#if errors[field]}
    <p id={errorId(field)} class="kw-error">{errors[field]}</p>
  {/if}
{/snippet}

<div class="kw-fields" data-sentry-mask>
  {#if type === "generic_secret"}
    <div>
      <label class="kw-label" for={id("secret")}
        >{tr("kwallet.field.secret")}</label
      >
      <textarea
        id={id("secret")}
        class="kw-input kw-mono"
        rows="4"
        autocomplete="off"
        autocapitalize="off"
        spellcheck="false"
        required
        {disabled}
        aria-invalid={errors.secret ? "true" : undefined}
        aria-describedby={describedBy("secret")}
        bind:value={draft.secret}
      ></textarea>
      {@render fieldError("secret")}
    </div>
  {:else if type === "variable"}
    <div>
      <label class="kw-label" for={id("value")}
        >{tr("kwallet.field.value")}</label
      >
      <input
        id={id("value")}
        class="kw-input kw-mono"
        type="text"
        autocomplete="off"
        autocapitalize="off"
        spellcheck="false"
        required
        {disabled}
        aria-invalid={errors.value ? "true" : undefined}
        aria-describedby={describedBy("value")}
        bind:value={draft.value}
      />
      {@render fieldError("value")}
    </div>
  {:else if type === "configuration"}
    <div>
      <label class="kw-label" for={id("document")}
        >{tr("kwallet.field.document")}</label
      >
      <textarea
        id={id("document")}
        class="kw-input kw-mono"
        rows="8"
        autocomplete="off"
        autocapitalize="off"
        spellcheck="false"
        required
        {disabled}
        aria-invalid={errors.document ? "true" : undefined}
        aria-describedby={describedBy("document")}
        bind:value={draft.document}
      ></textarea>
      {@render fieldError("document")}
    </div>
  {:else if type === "login"}
    <div>
      <label class="kw-label" for={id("username")}
        >{tr("kwallet.field.username")}</label
      >
      <input
        id={id("username")}
        class="kw-input"
        type="text"
        autocomplete="off"
        autocapitalize="off"
        spellcheck="false"
        required
        {disabled}
        aria-invalid={errors.username ? "true" : undefined}
        aria-describedby={describedBy("username")}
        bind:value={draft.username}
      />
      {@render fieldError("username")}
    </div>
    <div>
      <label class="kw-label" for={id("password")}
        >{tr("kwallet.field.password")}</label
      >
      <input
        id={id("password")}
        class="kw-input"
        type="password"
        autocomplete="new-password"
        required
        {disabled}
        aria-invalid={errors.password ? "true" : undefined}
        aria-describedby={describedBy("password")}
        bind:value={draft.password}
      />
      {@render fieldError("password")}
    </div>
    <div>
      <label class="kw-label" for={id("totp")}>{tr("kwallet.field.totp")}</label
      >
      <input
        id={id("totp")}
        class="kw-input kw-mono"
        type="password"
        autocomplete="off"
        {disabled}
        bind:value={draft.totp}
      />
    </div>
  {:else if type === "node_access_profile"}
    <div>
      <label class="kw-label" for={id("host")}>{tr("kwallet.field.host")}</label
      >
      <input
        id={id("host")}
        class="kw-input kw-mono"
        type="text"
        autocomplete="off"
        autocapitalize="off"
        spellcheck="false"
        required
        disabled={locked("host")}
        aria-invalid={errors.host ? "true" : undefined}
        aria-describedby={describedBy("host", true)}
        bind:value={draft.host}
      />
      <p id={hintId("host")} class="kw-hint">{tr("kwallet.field.host_hint")}</p>
      {@render fieldError("host")}
    </div>
    <div>
      <label class="kw-label" for={id("port")}>{tr("kwallet.field.port")}</label
      >
      <input
        id={id("port")}
        class="kw-input kw-mono"
        type="text"
        inputmode="numeric"
        autocomplete="off"
        placeholder="22"
        disabled={locked("port")}
        aria-invalid={errors.port ? "true" : undefined}
        aria-describedby={describedBy("port")}
        bind:value={draft.port}
      />
      {@render fieldError("port")}
    </div>
    <div>
      <label class="kw-label" for={id("user")}>{tr("kwallet.field.user")}</label
      >
      <input
        id={id("user")}
        class="kw-input"
        type="text"
        autocomplete="off"
        autocapitalize="off"
        spellcheck="false"
        required
        disabled={locked("user")}
        aria-invalid={errors.user ? "true" : undefined}
        aria-describedby={describedBy("user")}
        bind:value={draft.user}
      />
      {@render fieldError("user")}
    </div>
    <div role="radiogroup" aria-label={tr("kwallet.field.auth")}>
      <span class="kw-label">{tr("kwallet.field.auth")}</span>
      <div class="kw-choice">
        <label>
          <input
            type="radio"
            name={`${idPrefix}-auth`}
            value="privateKey"
            checked={draft.nodeAuth === "privateKey"}
            disabled={disabled || lockAuth}
            onchange={() => (draft.nodeAuth = "privateKey")}
          />
          {tr("kwallet.auth.private_key")}
        </label>
        <label>
          <input
            type="radio"
            name={`${idPrefix}-auth`}
            value="password"
            checked={draft.nodeAuth === "password"}
            disabled={disabled || lockAuth}
            onchange={() => (draft.nodeAuth = "password")}
          />
          {tr("kwallet.auth.password")}
        </label>
      </div>
    </div>
    {#if draft.nodeAuth === "privateKey"}
      <div>
        <label class="kw-label" for={id("privateKey")}
          >{tr("kwallet.field.private_key")}</label
        >
        <textarea
          id={id("privateKey")}
          class="kw-input kw-mono"
          rows="6"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          required
          {disabled}
          aria-invalid={errors.privateKey ? "true" : undefined}
          aria-describedby={describedBy("privateKey")}
          bind:value={draft.privateKey}
        ></textarea>
        {@render fieldError("privateKey")}
      </div>
      <div>
        <label class="kw-label" for={id("publicKey")}
          >{tr("kwallet.field.public_key_optional")}</label
        >
        <input
          id={id("publicKey")}
          class="kw-input kw-mono"
          type="text"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          {disabled}
          aria-invalid={errors.publicKey ? "true" : undefined}
          aria-describedby={describedBy("publicKey", true)}
          bind:value={draft.publicKey}
        />
        <p id={hintId("publicKey")} class="kw-hint">
          {tr("kwallet.field.public_key_hint")}
        </p>
        {@render fieldError("publicKey")}
      </div>
    {:else}
      <div>
        <label class="kw-label" for={id("password")}
          >{tr("kwallet.field.password")}</label
        >
        <input
          id={id("password")}
          class="kw-input"
          type="password"
          autocomplete="new-password"
          required
          {disabled}
          aria-invalid={errors.password ? "true" : undefined}
          aria-describedby={describedBy("password")}
          bind:value={draft.password}
        />
        {@render fieldError("password")}
      </div>
    {/if}
  {/if}
</div>
