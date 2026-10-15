<script lang="ts">
  /**
   * The settings of one Wallet item: its name and description, who may see its
   * value and, for an SSH certificate authority, what it may sign. Only the
   * members the owner changed are sent, through `configure`.
   */
  import { tick } from "svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import Button from "#lib/components/ui/Button.svelte";
  import { validDescription, validDisplayName } from "../item-types.js";
  import {
    composeSignPolicy,
    draftFromSignPolicy,
    emptySignPolicyDraft,
    isSshCa,
    itemSignPolicy,
    sameSignPolicy,
    type SignPolicyDraft,
    type SignPolicyErrors,
  } from "../ssh-ca.js";
  import { itemValueAccess, type ValueAccess } from "../item-settings.js";
  import {
    configureItem,
    type ConfigureItemInput,
    type WalletItem,
  } from "../wallet-api.js";
  import { walletErrorMessage } from "../wallet-text.js";
  import WalletDialog from "./WalletDialog.svelte";
  import WalletSignPolicyFields from "./WalletSignPolicyFields.svelte";
  import "./wallet.css";

  interface Props {
    item: WalletItem;
    onclose: () => void;
    ondone: (result: { name: string; auditPending: boolean }) => void;
    onerror?: (message: string) => void;
  }

  let { item, onclose, ondone, onerror }: Props = $props();

  const uid = $props.id();
  const idPrefix = `kwallet-settings-${uid}`;

  // The dialog mounts fresh for every open, so the item it opened with is fixed.
  const initial = (() => ({
    displayName: item.displayName,
    description: item.description ?? "",
    valueAccess: itemValueAccess(item),
    signPolicy: itemSignPolicy(item),
  }))();
  const sshCa = $derived(isSshCa(item.itemType));

  let displayName = $state(initial.displayName);
  let description = $state(initial.description);
  let valueAccess = $state<ValueAccess>(initial.valueAccess);
  let policyDraft = $state<SignPolicyDraft>(
    initial.signPolicy
      ? draftFromSignPolicy(initial.signPolicy)
      : emptySignPolicyDraft(),
  );
  let policyErrors = $state<SignPolicyErrors>({});
  let nameError = $state<string | null>(null);
  let descriptionError = $state<string | null>(null);
  let submitting = $state(false);
  let failure = $state<string | null>(null);
  let closed = false;

  function close() {
    closed = true;
    onclose();
  }

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (submitting) return;
    failure = null;
    nameError = validDisplayName(displayName)
      ? null
      : tr("kwallet.error.name_invalid");
    descriptionError =
      !description || validDescription(description)
        ? null
        : tr("kwallet.error.description_invalid");
    const changes: ConfigureItemInput = {};
    if (sshCa) {
      const policy = composeSignPolicy(policyDraft);
      policyErrors = policy.ok ? {} : policy.errors;
      if (!policy.ok) {
        await tick();
        document
          .querySelector<HTMLElement>(
            `[id^="${idPrefix}-policy-"][aria-invalid="true"]`,
          )
          ?.focus();
        return;
      }
      if (
        !initial.signPolicy ||
        !sameSignPolicy(initial.signPolicy, policy.policy)
      )
        changes.signPolicy = policy.policy;
    } else if (valueAccess !== initial.valueAccess) {
      changes.policy = { valueAccess };
    }
    if (nameError || descriptionError) return;
    if (
      displayName !== initial.displayName ||
      description !== initial.description
    )
      changes.metadata = {
        displayName,
        scope: item.scope,
        ...(description ? { description } : {}),
        ...(item.name ? { name: item.name } : {}),
      };
    if (Object.keys(changes).length === 0) {
      close();
      return;
    }
    submitting = true;
    try {
      const result = await configureItem(item.itemId, changes);
      ondone({ name: displayName, auditPending: result.auditPending });
    } catch (error) {
      if (closed) onerror?.(walletErrorMessage(error));
      else failure = walletErrorMessage(error);
    } finally {
      submitting = false;
    }
  }
</script>

<WalletDialog
  title={tr("kwallet.settings.title", { name: item.displayName })}
  description={sshCa
    ? tr("kwallet.ssh_ca.settings_description")
    : tr("kwallet.settings.description")}
  onclose={close}
>
  <form class="kw-stack" novalidate onsubmit={submit}>
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
          disabled={submitting}
          aria-invalid={nameError ? "true" : undefined}
          bind:value={displayName}
        />
        {#if nameError}<p class="kw-error">{nameError}</p>{/if}
      </div>
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
          aria-invalid={descriptionError ? "true" : undefined}
          bind:value={description}
        />
        {#if descriptionError}<p class="kw-error">{descriptionError}</p>{/if}
      </div>
    </div>

    {#if sshCa}
      <WalletSignPolicyFields
        bind:draft={policyDraft}
        errors={policyErrors}
        {idPrefix}
        disabled={submitting}
      />
    {:else}
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
      <Button type="submit" variant="primary" size="sm" disabled={submitting}>
        {submitting ? tr("kwallet.working") : tr("kwallet.settings.save")}
      </Button>
    </div>
  </form>
</WalletDialog>
