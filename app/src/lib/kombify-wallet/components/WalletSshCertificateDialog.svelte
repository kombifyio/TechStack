<script lang="ts">
  /**
   * Sign an SSH certificate for yourself with one of your certificate
   * authorities (Wallet plan P3.1). The owner pastes a public key, chooses the
   * principals and the lifetime inside the authority's sign policy, and gets
   * the certificate line back to copy or download. Only public material is
   * involved: a pasted private key is refused and cleared here, never sent.
   */
  import { onDestroy, tick } from "svelte";
  import Dialog from "./WalletDialog.svelte";
  import { tr as t } from "#lib/i18n.svelte.js";
  import { getLocale } from "#lib/i18n.svelte.js";
  import { copyText, saveTextFile } from "../browser-io.js";
  import {
    composeSignRequest,
    defaultPrincipals,
    defaultSignDuration,
    emptySignDraft,
    formatDuration,
    itemSignPolicy,
    MAX_SIGN_PRINCIPALS,
    maxSeconds,
    type CertificateKind,
    type SignDraft,
    type SignErrors,
  } from "../ssh-ca.js";
  import {
    signCertificate,
    type IssuedCertificate,
    type WalletItem,
  } from "../wallet-api.js";
  import { walletErrorMessage } from "../wallet-text.js";

  interface Props {
    item: WalletItem;
    onclose: () => void;
  }

  let { item, onclose }: Props = $props();

  const uid = $props.id();
  const idPrefix = `wallet-sign-${uid}`;
  const id = (field: string) => `${idPrefix}-${field}`;

  // The dialog mounts fresh for every open, so the policy it opened with is fixed.
  const policy = (() => itemSignPolicy(item))();

  let draft = $state<SignDraft>(policy ? emptySignDraft(policy) : blank());
  let errors = $state<SignErrors>({});
  let submitting = $state(false);
  let failure = $state<string | null>(null);
  let issued = $state<(IssuedCertificate & { fileName: string }) | null>(null);
  let copied = $state(false);
  let copyFailed = $state(false);
  let saveFailed = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  function blank(): SignDraft {
    return {
      publicKey: "",
      kind: "user",
      principals: "",
      ttl: "1h",
      sourceAddress: "",
    };
  }

  const kinds = $derived(policy?.kinds ?? []);
  const maxLabel = $derived(
    policy ? formatDuration(maxSeconds(policy, draft.kind)) : "",
  );
  const sourceRequired = $derived(
    Boolean(policy?.requireSourceAddress) && draft.kind === "user",
  );

  /** A different kind has its own lifetime bound and its own kind of principal. */
  function chooseKind(kind: CertificateKind) {
    if (!policy || kind === draft.kind) return;
    const untouched = [defaultPrincipals(policy), ""].includes(
      draft.principals,
    );
    draft.kind = kind;
    draft.ttl = defaultSignDuration(policy, kind);
    if (untouched)
      draft.principals = kind === "user" ? defaultPrincipals(policy) : "";
    errors = {};
  }

  /** OpenSSH looks for `<key>-cert.pub` next to the private key it belongs to. */
  function fileNameFor(type: string): string {
    return `id_${type === "ssh-rsa" ? "rsa" : type === "ssh-ed25519" ? "ed25519" : "ecdsa"}-cert.pub`;
  }

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (submitting || !policy) return;
    failure = null;
    const composed = composeSignRequest(policy, draft);
    if (!composed.ok) {
      errors = composed.errors;
      // A private key never stays in the page, let alone leaves it.
      if (composed.discardKey) draft.publicKey = "";
      await tick();
      document
        .querySelector<HTMLElement>(`[id^="${idPrefix}-"][aria-invalid="true"]`)
        ?.focus();
      return;
    }
    errors = {};
    submitting = true;
    try {
      const answer = await signCertificate(item.itemId, composed);
      issued = {
        ...answer,
        fileName: fileNameFor(composed.subject.type),
      };
      copied = false;
      copyFailed = false;
      saveFailed = false;
    } catch (error) {
      failure = walletErrorMessage(error);
    } finally {
      submitting = false;
    }
  }

  function when(at: number): string {
    return new Intl.DateTimeFormat(getLocale(), {
      dateStyle: "medium",
      timeStyle: "short",
    }).format(new Date(at));
  }

  async function copy() {
    if (!issued) return;
    copyFailed = false;
    if (!(await copyText(issued.certificate))) {
      copyFailed = true;
      return;
    }
    copied = true;
    if (timer !== undefined) clearTimeout(timer);
    timer = setTimeout(() => (copied = false), 2000);
  }

  function download() {
    if (!issued) return;
    saveFailed = !saveTextFile(issued.fileName, `${issued.certificate}\n`);
  }

  function another() {
    issued = null;
    if (policy) draft = emptySignDraft(policy, draft.kind);
  }

  onDestroy(() => {
    if (timer !== undefined) clearTimeout(timer);
  });
</script>

<Dialog
  open={true}
  title={t("kwallet.sign.title", { name: item.displayName })}
  description={t("kwallet.sign.description")}
  {onclose}
  closeLabel={t("kwallet.close")}
>
  {#if !policy}
    <div class="sign">
      <p data-kx="status" data-status="warn" class="text-sm">
        {t("kwallet.ssh_ca.no_policy")}
      </p>
      <div class="sign__actions">
        <button
          type="button"
          class="kw-btn"
          data-kx="control"
          data-variant="primary"
          onclick={onclose}
        >
          {t("kwallet.close")}
        </button>
      </div>
    </div>
  {:else if issued}
    <div class="sign" data-testid="wallet-certificate">
      <p data-kx="status" data-status="ok" class="text-sm">
        {t("kwallet.sign.valid", {
          from: when(issued.validAfter),
          until: when(issued.validBefore),
        })}
      </p>
      <p class="text-sm">
        <span class="text-muted-foreground">
          {t("kwallet.sign.result_principals")}
        </span>
        <code data-testid="wallet-certificate-principals"
          >{issued.principals.join(", ")}</code
        >
      </p>
      <div>
        <label class="sign__label" for={id("certificate")}>
          {t("kwallet.sign.certificate")}
        </label>
        <textarea
          id={id("certificate")}
          class="kw-input sign__mono"
          rows="6"
          readonly
          spellcheck="false"
          data-testid="wallet-certificate-line"
          value={issued.certificate}
        ></textarea>
        <p class="text-muted-foreground mt-1 text-xs">
          {t("kwallet.sign.install_hint", { file: issued.fileName })}
        </p>
      </div>
      {#if issued.auditPending}
        <p
          data-kx="status"
          data-status="warn"
          class="text-muted-foreground text-sm"
        >
          {t("kwallet.notice.audit_pending")}
        </p>
      {/if}
      {#if copyFailed || saveFailed}
        <p role="alert" class="text-destructive text-sm">
          {t(
            copyFailed
              ? "kwallet.ssh_ca.copy_failed"
              : "kwallet.sign.download_failed",
          )}
        </p>
      {/if}
      <div class="sign__actions">
        <button
          type="button"
          class="kw-btn"
          data-kx="control"
          onclick={another}
        >
          {t("kwallet.sign.again")}
        </button>
        <button
          type="button"
          class="kw-btn"
          data-kx="control"
          onclick={download}
        >
          {t("kwallet.sign.download")}
        </button>
        <button
          type="button"
          class="kw-btn"
          data-kx="control"
          data-variant="primary"
          onclick={copy}
        >
          {copied ? t("kwallet.ssh_ca.copied") : t("kwallet.sign.copy")}
        </button>
      </div>
    </div>
  {:else}
    <form class="sign" novalidate onsubmit={submit}>
      <div data-sentry-mask>
        <label class="sign__label" for={id("publicKey")}>
          {t("kwallet.sign.public_key")}
        </label>
        <textarea
          id={id("publicKey")}
          class="kw-input sign__mono"
          rows="3"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          required
          disabled={submitting}
          aria-invalid={errors.publicKey ? "true" : undefined}
          aria-describedby={`${id("publicKey-hint")}${errors.publicKey ? ` ${id("publicKey-error")}` : ""}`}
          bind:value={draft.publicKey}
        ></textarea>
        <p id={id("publicKey-hint")} class="text-muted-foreground mt-1 text-xs">
          {t("kwallet.sign.public_key_hint")}
        </p>
        {#if errors.publicKey}
          <p
            id={id("publicKey-error")}
            role="alert"
            class="text-destructive mt-1 text-xs"
          >
            {t(
              errors.publicKey === "private_key"
                ? "kwallet.sign.error.private_key"
                : errors.publicKey === "invalid"
                  ? "kwallet.sign.error.key_invalid"
                  : "kwallet.sign.error.key_required",
            )}
          </p>
        {/if}
      </div>

      {#if kinds.length > 1}
        <fieldset class="sign__kinds" disabled={submitting}>
          <legend class="sign__label">{t("kwallet.sign.kind")}</legend>
          {#each kinds as kind (kind)}
            <label class="sign__option">
              <input
                type="radio"
                name={id("kind")}
                value={kind}
                checked={draft.kind === kind}
                onchange={() => chooseKind(kind)}
              />
              {t(
                kind === "user"
                  ? "kwallet.sign.kind_user"
                  : "kwallet.sign.kind_host",
              )}
            </label>
          {/each}
        </fieldset>
      {/if}

      <div>
        <label class="sign__label" for={id("principals")}>
          {t("kwallet.sign.principals")}
        </label>
        <input
          id={id("principals")}
          class="kw-input sign__mono"
          type="text"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          required
          disabled={submitting}
          aria-invalid={errors.principals ? "true" : undefined}
          aria-describedby={`${id("principals-hint")}${errors.principals ? ` ${id("principals-error")}` : ""}`}
          bind:value={draft.principals}
        />
        <p
          id={id("principals-hint")}
          class="text-muted-foreground mt-1 text-xs"
        >
          {t("kwallet.sign.principals_hint", {
            allowed: policy.principals.join(", "),
          })}
        </p>
        {#if errors.principals}
          <p id={id("principals-error")} class="text-destructive mt-1 text-xs">
            {#if errors.principals === "required"}
              {t("kwallet.ssh_ca.error.principals_required")}
            {:else if errors.principals === "too_many"}
              {t("kwallet.sign.error.principals_too_many", {
                max: String(MAX_SIGN_PRINCIPALS),
              })}
            {:else if errors.principals === "outside"}
              {t("kwallet.sign.error.principal_outside", {
                principal: errors.principal ?? "",
              })}
            {:else}
              {t("kwallet.sign.error.principal_invalid", {
                principal: errors.principal ?? "",
              })}
            {/if}
          </p>
        {/if}
      </div>

      <div>
        <label class="sign__label" for={id("ttl")}>
          {t("kwallet.sign.duration")}
        </label>
        <input
          id={id("ttl")}
          class="kw-input sign__duration"
          type="text"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          required
          disabled={submitting}
          aria-invalid={errors.ttl ? "true" : undefined}
          aria-describedby={`${id("ttl-hint")}${errors.ttl ? ` ${id("ttl-error")}` : ""}`}
          bind:value={draft.ttl}
        />
        <p id={id("ttl-hint")} class="text-muted-foreground mt-1 text-xs">
          {t("kwallet.sign.duration_hint", { max: maxLabel })}
        </p>
        {#if errors.ttl}
          <p id={id("ttl-error")} class="text-destructive mt-1 text-xs">
            {t("kwallet.sign.error.ttl_invalid", { max: maxLabel })}
          </p>
        {/if}
      </div>

      {#if draft.kind === "user"}
        <div>
          <label class="sign__label" for={id("sourceAddress")}>
            {t("kwallet.sign.source")}
          </label>
          <input
            id={id("sourceAddress")}
            class="kw-input sign__mono"
            type="text"
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
            required={sourceRequired}
            disabled={submitting}
            aria-invalid={errors.sourceAddress ? "true" : undefined}
            aria-describedby={`${id("sourceAddress-hint")}${errors.sourceAddress ? ` ${id("sourceAddress-error")}` : ""}`}
            bind:value={draft.sourceAddress}
          />
          <p
            id={id("sourceAddress-hint")}
            class="text-muted-foreground mt-1 text-xs"
          >
            {t(
              sourceRequired
                ? "kwallet.sign.source_hint_required"
                : "kwallet.sign.source_hint",
            )}
          </p>
          {#if errors.sourceAddress}
            <p
              id={id("sourceAddress-error")}
              class="text-destructive mt-1 text-xs"
            >
              {t(
                errors.sourceAddress === "required"
                  ? "kwallet.sign.error.source_required"
                  : "kwallet.sign.error.source_invalid",
              )}
            </p>
          {/if}
        </div>
      {/if}

      {#if failure}
        <p
          role="alert"
          data-kx="status"
          data-status="error"
          class="text-destructive text-sm"
        >
          {failure}
        </p>
      {/if}

      <div class="sign__actions">
        <button
          type="button"
          class="kw-btn"
          data-kx="control"
          onclick={onclose}
        >
          {t("kwallet.cancel")}
        </button>
        <button
          type="submit"
          class="kw-btn"
          data-kx="control"
          data-variant="primary"
          disabled={submitting}
          aria-busy={submitting ? "true" : undefined}
        >
          {submitting ? t("kwallet.working") : t("kwallet.sign.submit")}
        </button>
      </div>
    </form>
  {/if}
</Dialog>

<style>
  .sign {
    display: grid;
    gap: 1rem;
  }
  .sign__label {
    display: block;
    margin-bottom: 0.25rem;
    font-size: 0.85rem;
    font-weight: 500;
    color: var(--muted-foreground);
  }
  .sign__mono {
    font-family: var(--font-mono, ui-monospace, monospace);
    font-size: 0.85rem;
  }
  .sign__duration {
    width: 10rem;
    max-width: 100%;
  }
  .sign__kinds {
    display: grid;
    gap: 0.35rem;
    margin: 0;
    padding: 0;
    border: 0;
    min-width: 0;
  }
  .sign__option {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.9rem;
  }
  .sign__actions {
    display: flex;
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: 0.5rem;
  }
</style>
