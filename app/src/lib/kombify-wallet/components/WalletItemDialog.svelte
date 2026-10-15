<script lang="ts" module>
  export type WalletItemDialogResult = {
    kind: "created" | "versioned";
    name: string;
    itemId: string;
    auditPending: boolean;
    /** Whether Techstack was allowed to use the new item, when that was asked. */
    techstackAccess: "granted" | "failed" | "not-requested";
  };
</script>

<script lang="ts">
  /**
   * Add a Wallet item, or a new version of one. The value is composed from the
   * type's fields, encrypted in this browser to the Wallet Store's capture key,
   * and only the JWE is sent. Field contents are wiped after a save and
   * whenever the dialog closes. An SSH certificate authority is made inside the
   * Wallet and has no value to send, unless an existing key is imported.
   */
  import { onDestroy, onMount, tick } from "svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import Button from "#lib/components/ui/Button.svelte";
  import {
    EDITOR_ITEM_TYPES,
    clearValueDraft,
    composeScope,
    composeValue,
    describeNodeAccess,
    emptyValueDraft,
    isEditorItemType,
    isNamedItemType,
    parseScope,
    validDescription,
    validDisplayName,
    validItemName,
    type EditorItemType,
    type ScopeKind,
    type ValueDraft,
    type ValueFieldName,
  } from "../item-types.js";
  import {
    composeSignPolicy,
    emptySignPolicyDraft,
    isSshCa,
    type SignPolicy,
    type SignPolicyDraft,
    type SignPolicyErrors,
  } from "../ssh-ca.js";
  import {
    addVersion,
    createItem,
    setServiceAccess,
    type WalletItem,
  } from "../wallet-api.js";
  import { itemTypeLabel, walletErrorMessage } from "../wallet-text.js";
  import { TECHSTACK_SERVICE } from "../service-access.js";
  import { DEFAULT_VALUE_ACCESS, type ValueAccess } from "../item-settings.js";
  import { copyText } from "../browser-io.js";
  import {
    generateSSHKey,
    isSSHKeyGenAvailable,
  } from "#lib/wallet/sshKeygen.js";
  import WalletDialog from "./WalletDialog.svelte";
  import WalletSignPolicyFields from "./WalletSignPolicyFields.svelte";
  import WalletSshCaSource from "./WalletSshCaSource.svelte";
  import WalletValueFields from "./WalletValueFields.svelte";
  import "./wallet.css";

  interface Props {
    mode: "create" | "version";
    item?: WalletItem | null;
    ownerSubject: string;
    /** Create inside this scope (a stack page) instead of asking for one. */
    fixedScope?: string;
    /** Types offered when creating; default all editor types. */
    allowedTypes?: readonly EditorItemType[];
    initialType?: EditorItemType;
    initialName?: string;
    initialDraft?: Partial<ValueDraft>;
    /** Value fields the caller already knows (the wizard's host and user). */
    lockedFields?: ValueFieldName[];
    /** The sign-in method of a server access item is already decided. */
    lockAuth?: boolean;
    /** Generate an SSH key pair as soon as the dialog opens. */
    generateKey?: boolean;
    /** Whether the "Techstack may use this" box starts checked. */
    allowTechstackByDefault?: boolean;
    /** Ask for the product assignment at all (Techstack's own surfaces). */
    offerTechstackAccess?: boolean;
    onclose: () => void;
    ondone: (result: WalletItemDialogResult) => void;
    /** A save that fails after the dialog was dismissed reports here. */
    onerror?: (message: string) => void;
  }

  let {
    mode,
    item = null,
    ownerSubject,
    fixedScope,
    allowedTypes = EDITOR_ITEM_TYPES,
    initialType,
    initialName = "",
    initialDraft,
    lockedFields = [],
    lockAuth = false,
    generateKey = false,
    allowTechstackByDefault,
    offerTechstackAccess = true,
    onclose,
    ondone,
    onerror,
  }: Props = $props();

  const uid = $props.id();
  const idPrefix = `kwallet-${uid}`;
  const formId = `${idPrefix}-form`;

  const SCOPE_OPTIONS = [
    ["account", "kwallet.scope_option.account"],
    ["project", "kwallet.scope_option.project"],
    ["environment", "kwallet.scope_option.environment"],
  ] as const;

  const TYPE_HINTS: Record<EditorItemType, string> = {
    generic_secret: "kwallet.type_hint.generic_secret",
    login: "kwallet.type_hint.login",
    variable: "kwallet.type_hint.variable",
    configuration: "kwallet.type_hint.configuration",
    node_access_profile: "kwallet.type_hint.node_access_profile",
    ssh_ca: "kwallet.type_hint.ssh_ca",
  };

  // The dialog mounts fresh for every open, so what it opened with is fixed.
  const initial = (() => {
    const type: EditorItemType =
      mode === "version" && item && isEditorItemType(item.itemType)
        ? item.itemType
        : (initialType ?? allowedTypes[0] ?? "generic_secret");
    return {
      type,
      name: initialName,
      draft: { ...emptyValueDraft(), ...initialDraft },
      scope: fixedScope ? parseScope(fixedScope) : null,
      allowTechstack:
        allowTechstackByDefault ?? initialType === "node_access_profile",
    };
  })();

  let itemType = $state<EditorItemType>(initial.type);
  let displayName = $state(initial.name);
  // Variables and configurations resolve under this name (`DATABASE_URL`).
  let itemName = $state("");
  let description = $state("");
  let scopeKind = $state<ScopeKind>(initial.scope?.kind ?? "account");
  let project = $state(
    initial.scope && initial.scope.kind !== "account"
      ? initial.scope.project
      : "",
  );
  let environment = $state(
    initial.scope?.kind === "environment" ? initial.scope.environment : "",
  );
  let draft = $state<ValueDraft>(initial.draft);
  type FieldName =
    | ValueFieldName
    | "name"
    | "itemName"
    | "description"
    | "project"
    | "environment";
  let errors = $state<Partial<Record<FieldName, string>>>({});
  let valueAccess = $state<ValueAccess>(DEFAULT_VALUE_ACCESS);
  let policyDraft = $state<SignPolicyDraft>(emptySignPolicyDraft());
  let policyErrors = $state<SignPolicyErrors>({});
  let allowTechstack = $state(initial.allowTechstack);
  let submitting = $state(false);
  let failure = $state<string | null>(null);
  let generating = $state(false);
  let generatedPublicKey = $state("");
  let publicKeyCopied = $state(false);

  const title = $derived(
    mode === "create"
      ? tr("kwallet.create.title")
      : tr("kwallet.version.title", { name: item?.displayName ?? "" }),
  );
  const namedType = $derived(isNamedItemType(itemType) ? itemType : null);
  const sshCa = $derived(isSshCa(itemType));
  // A CA the Wallet makes has no value: nothing is typed, encrypted or sent.
  const walletGenerates = $derived(sshCa && draft.caSource === "generate");
  const lead = $derived(
    mode === "create"
      ? tr("kwallet.create.description")
      : tr("kwallet.version.description"),
  );
  const canGenerateKey = $derived(
    itemType === "node_access_profile" &&
      draft.nodeAuth === "privateKey" &&
      isSSHKeyGenAvailable(),
  );

  let closed = false;

  /**
   * Always closes: the modal also closes on Escape, so the state must follow.
   * A save already sent still reports its outcome to the page.
   */
  function close() {
    closed = true;
    clearValueDraft(draft);
    generatedPublicKey = "";
    onclose();
  }

  onDestroy(() => clearValueDraft(draft));

  /** A new Ed25519 pair, made in this browser; the private half goes into the draft. */
  async function generatePair() {
    if (generating) return;
    generating = true;
    failure = null;
    try {
      const pair = await generateSSHKey("ed25519");
      draft.nodeAuth = "privateKey";
      draft.privateKey = pair.privateKey;
      draft.publicKey = pair.publicKey;
      generatedPublicKey = pair.publicKey;
      publicKeyCopied = false;
    } catch {
      failure = tr("kwallet.error.keygen_unavailable");
    } finally {
      generating = false;
    }
  }

  onMount(() => {
    if (generateKey) void generatePair();
  });

  async function copyPublicKey() {
    publicKeyCopied = await copyText(generatedPublicKey);
  }

  /**
   * What is stored as the item's description. A server access item opens with
   * the host, user and sign-in method it holds, so the item can be matched to a
   * server without being opened; the owner's own note follows.
   */
  function storedDescription(): string {
    if (itemType !== "node_access_profile") return description;
    const port = Number(draft.port.trim());
    const line = describeNodeAccess(
      {
        host: draft.host.trim(),
        user: draft.user.trim() || "root",
        ...(Number.isInteger(port) && port > 0 ? { port } : {}),
        method: draft.nodeAuth,
      },
      description,
    );
    return line;
  }

  function validate(): Partial<Record<FieldName, string>> {
    const found: Partial<Record<FieldName, string>> = {};
    if (mode === "create") {
      if (!displayName) found.name = tr("kwallet.error.required");
      else if (!validDisplayName(displayName))
        found.name = tr("kwallet.error.name_invalid");
      if (namedType) {
        if (!itemName) found.itemName = tr("kwallet.error.required");
        else if (!validItemName(namedType, itemName))
          found.itemName = tr(
            namedType === "variable"
              ? "kwallet.error.variable_name_invalid"
              : "kwallet.error.configuration_name_invalid",
          );
      }
      if (description && !validDescription(description))
        found.description = tr("kwallet.error.description_invalid");
      else if (storedDescription() && !validDescription(storedDescription()))
        found.description = tr("kwallet.error.description_invalid");
      if (!fixedScope) {
        const scope = composeScope(scopeKind, project, environment);
        if ("error" in scope) {
          if (scope.error === "project_invalid")
            found.project = tr("kwallet.error.segment_invalid");
          else found.environment = tr("kwallet.error.segment_invalid");
        }
      }
    }
    const value = walletGenerates ? null : composeValue(itemType, draft);
    if (value && "error" in value)
      found[value.field] =
        value.error === "host_invalid"
          ? tr("kwallet.error.host_invalid")
          : value.error === "port_invalid"
            ? tr("kwallet.error.port_invalid")
            : value.error === "public_key_invalid"
              ? tr("kwallet.error.public_key_invalid")
              : value.error === "ca_key_invalid"
                ? tr("kwallet.error.ca_key_invalid")
                : value.error === "ca_key_encrypted"
                  ? tr("kwallet.error.ca_key_encrypted")
                  : tr("kwallet.error.required");
    return found;
  }

  async function focusFirstInvalid(found: Partial<Record<FieldName, string>>) {
    await tick();
    const order: FieldName[] = [
      "name",
      "itemName",
      "description",
      "project",
      "environment",
      "secret",
      "value",
      "document",
      "username",
      "password",
      "host",
      "port",
      "user",
      "privateKey",
      "publicKey",
    ];
    const first = order.find((field) => found[field]);
    if (first) document.getElementById(`${idPrefix}-${first}`)?.focus();
  }

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (submitting) return;
    failure = null;
    const found = validate();
    errors = found;
    const policy =
      mode === "create" && sshCa ? composeSignPolicy(policyDraft) : null;
    policyErrors = policy && !policy.ok ? policy.errors : {};
    if (Object.keys(found).length > 0) {
      await focusFirstInvalid(found);
      return;
    }
    if (policy && !policy.ok) {
      await tick();
      document
        .querySelector<HTMLElement>(
          `[id^="${idPrefix}-policy-"][aria-invalid="true"]`,
        )
        ?.focus();
      return;
    }
    const signPolicy: SignPolicy | undefined = policy?.ok
      ? policy.policy
      : undefined;
    const composed = walletGenerates
      ? { value: null }
      : composeValue(itemType, draft);
    if ("error" in composed) return;
    submitting = true;
    try {
      if (mode === "create") {
        const scope = fixedScope
          ? { scope: fixedScope }
          : composeScope(scopeKind, project, environment);
        if ("error" in scope) return;
        const name = displayName;
        const result = await createItem({
          itemType,
          displayName: name,
          scope: scope.scope,
          ...(storedDescription() ? { description: storedDescription() } : {}),
          ...(namedType ? { name: itemName } : {}),
          value: composed.value,
          ownerSubject,
          ...(sshCa ? {} : { policy: { valueAccess } }),
          ...(signPolicy ? { signPolicy } : {}),
        });
        clearValueDraft(draft);
        let techstackAccess: WalletItemDialogResult["techstackAccess"] =
          "not-requested";
        if (offerTechstackAccess && allowTechstack && !sshCa) {
          try {
            techstackAccess = (await setServiceAccess(
              result.itemId,
              TECHSTACK_SERVICE,
              "use",
              true,
            ))
              ? "granted"
              : "failed";
          } catch {
            techstackAccess = "failed";
          }
        }
        ondone({
          kind: "created",
          name,
          itemId: result.itemId,
          auditPending: result.auditPending,
          techstackAccess,
        });
      } else if (item) {
        if (composed.value === null) return;
        const result = await addVersion(item, composed.value, ownerSubject);
        clearValueDraft(draft);
        ondone({
          kind: "versioned",
          name: item.displayName,
          itemId: item.itemId,
          auditPending: result.auditPending,
          techstackAccess: "not-requested",
        });
      }
    } catch (error) {
      if (closed) onerror?.(walletErrorMessage(error));
      else failure = walletErrorMessage(error);
    } finally {
      submitting = false;
    }
  }
</script>

<WalletDialog {title} description={lead} onclose={close}>
  <form id={formId} class="kw-stack" novalidate onsubmit={submit}>
    {#if mode === "create" && allowedTypes.length > 1}
      <fieldset class="kw-fields" disabled={submitting}>
        <legend class="kw-label">{tr("kwallet.field.type")}</legend>
        <div class="kw-choice">
          {#each allowedTypes as type (type)}
            <label>
              <input
                type="radio"
                name={`${idPrefix}-type`}
                value={type}
                checked={itemType === type}
                onchange={() => {
                  clearValueDraft(draft);
                  errors = {};
                  generatedPublicKey = "";
                  itemType = type;
                  if (
                    type === "node_access_profile" &&
                    allowTechstackByDefault === undefined
                  )
                    allowTechstack = true;
                }}
              />
              {itemTypeLabel(type)}
            </label>
          {/each}
        </div>
        <p class="kw-hint">{tr(TYPE_HINTS[itemType])}</p>
      </fieldset>
    {/if}

    {#if mode === "create"}
      <div class="kw-fields">
        <div>
          <label class="kw-label" for={`${idPrefix}-name`}
            >{tr("kwallet.field.name")}</label
          >
          <input
            id={`${idPrefix}-name`}
            class="kw-input"
            type="text"
            autocomplete="off"
            required
            disabled={submitting}
            aria-invalid={errors.name ? "true" : undefined}
            bind:value={displayName}
          />
          <p class="kw-hint">{tr("kwallet.field.name_hint")}</p>
          {#if errors.name}<p class="kw-error">{errors.name}</p>{/if}
        </div>

        {#if namedType}
          <div>
            <label class="kw-label" for={`${idPrefix}-itemName`}>
              {tr(
                namedType === "variable"
                  ? "kwallet.field.variable_name"
                  : "kwallet.field.configuration_name",
              )}
            </label>
            <input
              id={`${idPrefix}-itemName`}
              class="kw-input kw-mono"
              type="text"
              autocomplete="off"
              autocapitalize="off"
              spellcheck="false"
              required
              disabled={submitting}
              aria-invalid={errors.itemName ? "true" : undefined}
              bind:value={itemName}
            />
            <p class="kw-hint">
              {tr(
                namedType === "variable"
                  ? "kwallet.field.variable_name_hint"
                  : "kwallet.field.configuration_name_hint",
              )}
            </p>
            {#if errors.itemName}<p class="kw-error">{errors.itemName}</p>{/if}
          </div>
        {/if}

        <div>
          <label class="kw-label" for={`${idPrefix}-description`}
            >{tr("kwallet.field.description")}</label
          >
          <input
            id={`${idPrefix}-description`}
            class="kw-input"
            type="text"
            autocomplete="off"
            disabled={submitting}
            aria-invalid={errors.description ? "true" : undefined}
            bind:value={description}
          />
          {#if errors.description}<p class="kw-error">
              {errors.description}
            </p>{/if}
        </div>

        {#if !fixedScope}
          <fieldset class="kw-fields" disabled={submitting}>
            <legend class="kw-label">{tr("kwallet.field.scope")}</legend>
            <div class="kw-choice">
              {#each SCOPE_OPTIONS as [kind, label] (kind)}
                <label>
                  <input
                    type="radio"
                    name={`${idPrefix}-scope`}
                    value={kind}
                    checked={scopeKind === kind}
                    onchange={() => (scopeKind = kind)}
                  />
                  {tr(label)}
                </label>
              {/each}
            </div>
            {#if scopeKind !== "account"}
              <div>
                <label class="kw-label" for={`${idPrefix}-project`}
                  >{tr("kwallet.field.project")}</label
                >
                <input
                  id={`${idPrefix}-project`}
                  class="kw-input kw-mono"
                  type="text"
                  autocomplete="off"
                  autocapitalize="off"
                  spellcheck="false"
                  aria-invalid={errors.project ? "true" : undefined}
                  bind:value={project}
                />
                <p class="kw-hint">{tr("kwallet.field.segment_hint")}</p>
                {#if errors.project}<p class="kw-error">
                    {errors.project}
                  </p>{/if}
              </div>
            {/if}
            {#if scopeKind === "environment"}
              <div>
                <label class="kw-label" for={`${idPrefix}-environment`}
                  >{tr("kwallet.field.environment")}</label
                >
                <input
                  id={`${idPrefix}-environment`}
                  class="kw-input kw-mono"
                  type="text"
                  autocomplete="off"
                  autocapitalize="off"
                  spellcheck="false"
                  aria-invalid={errors.environment ? "true" : undefined}
                  bind:value={environment}
                />
                {#if errors.environment}<p class="kw-error">
                    {errors.environment}
                  </p>{/if}
              </div>
            {/if}
          </fieldset>
        {:else}
          <p class="kw-hint" data-testid="wallet-fixed-scope">
            {tr("kwallet.field.scope_fixed")}
          </p>
        {/if}
      </div>
    {/if}

    {#if sshCa && mode === "create"}
      <WalletSshCaSource
        bind:draft
        error={errors.privateKey}
        {idPrefix}
        disabled={submitting}
      />
      <WalletSignPolicyFields
        bind:draft={policyDraft}
        errors={policyErrors}
        {idPrefix}
        disabled={submitting}
      />
    {:else if !walletGenerates}
      <WalletValueFields
        type={itemType}
        bind:draft
        {errors}
        {idPrefix}
        disabled={submitting}
        {lockedFields}
        {lockAuth}
      />
    {/if}

    {#if canGenerateKey}
      <div class="kw-fields">
        <div>
          <Button
            variant="secondary"
            size="sm"
            disabled={submitting || generating}
            testId="wallet-generate-key"
            onclick={() => void generatePair()}
          >
            {generating
              ? tr("kwallet.keygen.generating")
              : tr("kwallet.keygen.generate")}
          </Button>
          <p class="kw-hint">{tr("kwallet.keygen.hint")}</p>
        </div>
        {#if generatedPublicKey}
          <div data-testid="wallet-generated-public-key">
            <span class="kw-label">{tr("kwallet.keygen.public_key")}</span>
            <code class="kw-secret">{generatedPublicKey}</code>
            <p class="kw-hint">{tr("kwallet.keygen.install_hint")}</p>
            <Button
              variant="secondary"
              size="sm"
              onclick={() => void copyPublicKey()}
            >
              {publicKeyCopied
                ? tr("kwallet.keygen.copied")
                : tr("kwallet.keygen.copy")}
            </Button>
          </div>
        {/if}
      </div>
    {/if}

    {#if mode === "create" && !sshCa}
      <fieldset class="kw-fields" disabled={submitting}>
        <legend class="kw-label">{tr("kwallet.field.value_access")}</legend>
        <div class="kw-choice">
          <label>
            <input
              type="radio"
              name={`${idPrefix}-access`}
              value="read_right_holders"
              checked={valueAccess === "read_right_holders"}
              onchange={() => (valueAccess = "read_right_holders")}
            />
            {tr("kwallet.value_access.read_right_holders")}
          </label>
          <label>
            <input
              type="radio"
              name={`${idPrefix}-access`}
              value="use_only"
              checked={valueAccess === "use_only"}
              onchange={() => (valueAccess = "use_only")}
            />
            {tr("kwallet.value_access.use_only")}
          </label>
        </div>
        <p class="kw-hint">
          {tr(
            valueAccess === "use_only"
              ? "kwallet.value_access.use_only_hint"
              : "kwallet.value_access.read_right_holders_hint",
          )}
        </p>
      </fieldset>

      {#if offerTechstackAccess}
        <label class="kw-check">
          <input
            type="checkbox"
            data-testid="wallet-allow-techstack"
            bind:checked={allowTechstack}
            disabled={submitting}
          />
          <span>
            {tr("kwallet.techstack.allow_label")}
            <span class="kw-hint kw-block"
              >{tr("kwallet.techstack.allow_hint")}</span
            >
          </span>
        </label>
      {/if}
    {/if}

    {#if failure}
      <p role="alert" class="kw-notice" data-tone="error">{failure}</p>
    {/if}

    <div class="kw-actions">
      <Button
        variant="secondary"
        size="sm"
        disabled={submitting}
        onclick={close}
      >
        {tr("kwallet.cancel")}
      </Button>
      <Button
        type="submit"
        variant="primary"
        size="sm"
        disabled={submitting}
        testId="wallet-save"
      >
        {submitting
          ? tr("kwallet.saving")
          : mode === "create"
            ? tr("kwallet.save")
            : tr("kwallet.version.save")}
      </Button>
    </div>
  </form>
</WalletDialog>
