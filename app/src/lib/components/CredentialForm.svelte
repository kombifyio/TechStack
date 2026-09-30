<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import type {
    CredentialType,
    WalletItem,
    WalletEntryArea,
  } from "#lib/wallet/types.js";
  import { parseApiError } from "#lib/api/errors.js";
  import { buildWalletEntryPayload } from "#lib/wallet/payload.js";

  // Props
  interface Props {
    credential?: Partial<WalletItem>;
    mode?: WalletEntryArea;
    onSave: (data: Partial<WalletItem>) => Promise<void>;
    onCancel: () => void;
    saving?: boolean;
  }

  let { credential, mode, onSave, onCancel, saving = false }: Props = $props();

  function inferWalletArea(item?: Partial<WalletItem>): WalletEntryArea {
    if (item?.item_class === "launch" || item?.access_mode === "open") {
      return "tools";
    }

    if (item?.item_class === "user_account" || item?.access_mode === "manage") {
      return "access";
    }

    return "recovery";
  }

  // Form state - credential prop is only read once on mount (modal pattern: component is recreated each time)
  // svelte-ignore state_referenced_locally
  let name = $state(credential?.name || "");
  // svelte-ignore state_referenced_locally
  let kind = $state<CredentialType>(credential?.kind || "password");
  // svelte-ignore state_referenced_locally
  let username = $state(credential?.username || "");
  // svelte-ignore state_referenced_locally
  let secret = $state(credential?.secret || "");
  // svelte-ignore state_referenced_locally
  let totp = $state(
    credential?.totp ||
      credential?.notes?.match(/TOTP:\s*([^\n]+)/i)?.[1] ||
      "",
  );
  // svelte-ignore state_referenced_locally
  let url = $state(credential?.url || "");
  // svelte-ignore state_referenced_locally
  let notes = $state(credential?.notes || "");
  // svelte-ignore state_referenced_locally
  let expiresAt = $state(credential?.expires_at?.split("T")[0] || "");
  // svelte-ignore state_referenced_locally
  let walletArea = $state<WalletEntryArea>(mode || inferWalletArea(credential));
  let error = $state<string | null>(null);

  const walletAreas: Array<{
    value: WalletEntryArea;
    label: string;
    description: string;
  }> = $derived([
    {
      value: "tools",
      label: tr("ui.wallet.tools"),
      description: tr("ui.credentialForm.administrativeToolsAndOperationalSurfaces"),
    },
    {
      value: "access",
      label: tr("ui.stacksCreatingCreationLease.access"),
      description: tr("ui.credentialForm.usersPortalsIpDeviceGating"),
    },
    {
      value: "recovery",
      label: tr("ui.stacksCreatingCreationCompletion.recovery"),
      description: tr("ui.credentialForm.breakGlassSecretsAndReveal"),
    },
  ]);

  const credentialTypes: Array<{
    value: CredentialType;
    label: string;
    abbr: string;
  }> = $derived([
    { value: "password", label: tr("ui.clientLocal.password"), abbr: "PW" },
    { value: "api_key", label: tr("ui.credentialForm.apiKey"), abbr: "API" },
    { value: "ssh_key", label: tr("ui.credentialForm.sshKey"), abbr: "SSH" },
    { value: "oauth_token", label: tr("ui.credentialForm.oauthToken"), abbr: "OAuth" },
    { value: "certificate", label: tr("ui.credentialForm.certificate"), abbr: tr("ui.credentialForm.cert") },
    { value: "other", label: tr("ui.wallet.other"), abbr: "?" },
  ]);

  function getSecretLabel(): string {
    switch (kind) {
      case "password":
        return tr("ui.clientLocal.password");
      case "api_key":
        return tr("ui.credentialForm.apiKey");
      case "ssh_key":
        return tr("ui.sSHKeyGenerator.privateKey");
      case "oauth_token":
        return tr("ui.credentialForm.token");
      case "certificate":
        return tr("ui.credentialForm.certificateContent");
      default:
        return tr("ui.wallet.secret");
    }
  }

  function getSecretPlaceholder(): string {
    switch (kind) {
      case "password":
        return tr("ui.credentialForm.enterPassword");
      case "api_key":
        return "sk_live_xxxxx...";
      case "ssh_key":
        return "-----BEGIN OPENSSH PRIVATE KEY-----\n...";
      case "oauth_token":
        return tr("ui.credentialForm.bearerTokenOrRefreshToken");
      case "certificate":
        return "-----BEGIN CERTIFICATE-----\n...";
      default:
        return tr("ui.credentialForm.enterSecretValue");
    }
  }

  function isMultilineSecret(): boolean {
    return kind === "ssh_key" || kind === "certificate";
  }

  function isSecretRequired(): boolean {
    return walletArea === "recovery" && kind !== "other";
  }

  function isUrlRequired(): boolean {
    return walletArea === "tools";
  }

  function getNamePlaceholder(): string {
    switch (walletArea) {
      case "tools":
        return tr("ui.credentialForm.eGPocketbaseAdmin");
      case "access":
        return tr("ui.credentialForm.eGTailscaleDeviceApproval");
      default:
        return tr("ui.credentialForm.eGBreakGlassEnvelope");
    }
  }

  function getUsernameLabel(): string {
    switch (walletArea) {
      case "tools":
        return kind === "api_key" ? tr("ui.credentialForm.apiLabelOwner") : tr("ui.credentialForm.operatorAccount");
      case "access":
        return tr("ui.credentialForm.userIdentity");
      default:
        return kind === "api_key"
          ? tr("ui.credentialForm.keyIdentifierLabel")
          : tr("ui.credentialForm.usernameEmail");
    }
  }

  function getUsernamePlaceholder(): string {
    if (walletArea === "access") {
      return "e.g., admin@kombify.io";
    }

    return kind === "api_key"
      ? "e.g., prod-api-key"
      : "e.g., admin@example.com";
  }

  function getUrlLabel(): string {
    switch (walletArea) {
      case "tools":
        return tr("ui.credentialForm.toolUrl");
      case "access":
        return tr("ui.credentialForm.portalPolicyUrl");
      default:
        return tr("ui.credentialForm.recoveryUrlEndpoint");
    }
  }

  function getUrlPlaceholder(): string {
    switch (walletArea) {
      case "tools":
        return "https://pocketbase.example.com/_/";
      case "access":
        return "https://id.example.com/admin";
      default:
        return "https://vault.example.com/recovery";
    }
  }

  function getNotesPlaceholder(): string {
    switch (walletArea) {
      case "tools":
        return tr("ui.credentialForm.whatThisToolIsFor");
      case "access":
        return tr("ui.credentialForm.describeTheUserGateOr");
      default:
        return tr("ui.credentialForm.describeTheRecoveryPathStorage");
    }
  }

  async function handleSubmit(e: Event) {
    e.preventDefault();
    error = null;

    if (!name.trim()) {
      error = tr("ui.credentialForm.nameIsRequired");
      return;
    }

    if (isUrlRequired() && !url.trim()) {
      error = tr("ui.credentialForm.toolUrlIsRequired");
      return;
    }

    if (
      walletArea === "access" &&
      !username.trim() &&
      !url.trim() &&
      !secret.trim() &&
      !totp.trim()
    ) {
      error = tr("ui.credentialForm.accessEntriesNeedAtLeast");
      return;
    }

    if (isSecretRequired() && !secret.trim()) {
      error = `${getSecretLabel()} is required`;
      return;
    }

    try {
      await onSave(
        buildWalletEntryPayload(walletArea, {
          name: name.trim(),
          kind,
          username: username.trim() || undefined,
          secret: secret.trim() || undefined,
          totp: totp.trim() || undefined,
          url: url.trim() || undefined,
          notes: notes.trim() || undefined,
          expires_at: expiresAt || undefined,
          source_type: credential?.source_type,
          source_ref: credential?.source_ref,
          kit_deployment_id: credential?.kit_deployment_id,
          service_id: credential?.service_id,
        }),
      );
    } catch (err) {
      const parsed = parseApiError(err);
      if (
        parsed.isValidationError &&
        Object.keys(parsed.fieldErrors).length > 0
      ) {
        error = Object.entries(parsed.fieldErrors)
          .map(([f, { message }]) => `${f}: ${message}`)
          .join("; ");
      } else {
        error =
          parsed.message ||
          (err instanceof Error ? err.message : tr("ui.credentialForm.failedToSaveCredential"));
      }
    }
  }
</script>

<form onsubmit={handleSubmit} class="space-y-6">
  {#if error}
    <div
      class="p-4 rounded-xl border border-destructive/30 bg-destructive/5 text-destructive"
    >
      {error}
    </div>
  {/if}

  <!-- Wallet Area -->
  <div>
    <span class="block text-sm font-medium text-foreground mb-2">
      {tr("ui.credentialForm.walletArea")} <span class="text-destructive">*</span>
    </span>
    <div class="grid grid-cols-1 md:grid-cols-3 gap-2">
      {#each walletAreas as area}
        <button
          type="button"
          onclick={() => (walletArea = area.value)}
          class="p-3 rounded-lg border text-left transition-colors {walletArea ===
          area.value
            ? 'bg-primary/10 border-primary/50 text-primary'
            : 'bg-card border-border text-muted-foreground hover:border-muted-foreground'}"
        >
          <div class="text-sm font-medium">{area.label}</div>
          <div class="text-xs mt-1 text-current/80">{area.description}</div>
        </button>
      {/each}
    </div>
  </div>

  <!-- Name -->
  <div>
    <label for="name" class="block text-sm font-medium text-foreground mb-2">
      {tr("ui.credentialForm.name")} <span class="text-destructive">*</span>
    </label>
    <input
      id="name"
      type="text"
      bind:value={name}
      placeholder={getNamePlaceholder()}
      class="w-full px-4 py-3 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:outline-none"
    />
  </div>

  <!-- Type -->
  <div>
    <span
      id="credential-type-label"
      class="block text-sm font-medium text-foreground mb-2"
    >
      {tr("ui.credentialForm.credentialType")} <span class="text-destructive">*</span>
    </span>
    <div
      class="grid grid-cols-2 md:grid-cols-3 gap-2"
      role="group"
      aria-labelledby="credential-type-label"
    >
      {#each credentialTypes as type}
        <button
          type="button"
          onclick={() => (kind = type.value)}
          class="p-3 rounded-lg border text-left transition-colors {kind ===
          type.value
            ? 'bg-primary/10 border-primary/50 text-primary'
            : 'bg-card border-border text-muted-foreground hover:border-muted-foreground'}"
        >
          <span class="text-xs font-mono mr-2 px-1.5 py-0.5 rounded bg-muted"
            >{type.abbr}</span
          >
          <span class="text-sm">{type.label}</span>
        </button>
      {/each}
    </div>
  </div>

  <!-- Username (optional, depending on type) -->
  {#if walletArea === "access" || kind === "password" || kind === "api_key"}
    <div>
      <label
        for="username"
        class="block text-sm font-medium text-foreground mb-2"
      >
        {getUsernameLabel()}
      </label>
      <input
        id="username"
        type="text"
        bind:value={username}
        placeholder={getUsernamePlaceholder()}
        class="w-full px-4 py-3 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:outline-none"
      />
    </div>
  {/if}

  <!-- Secret -->
  <div>
    <label for="secret" class="block text-sm font-medium text-foreground mb-2">
      {getSecretLabel()}
      {#if isSecretRequired()}
        <span class="text-destructive">*</span>
      {:else}
        <span class="text-muted-foreground">{tr("ui.credentialForm.optional")}</span>
      {/if}
    </label>
    {#if isMultilineSecret()}
      <textarea
        id="secret"
        bind:value={secret}
        rows="6"
        placeholder={getSecretPlaceholder()}
        class="w-full px-4 py-3 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:outline-none font-mono text-sm"
      ></textarea>
    {:else}
      <input
        id="secret"
        type="password"
        bind:value={secret}
        placeholder={getSecretPlaceholder()}
        class="w-full px-4 py-3 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:outline-none font-mono"
      />
    {/if}
  </div>

  <!-- TOTP (optional) -->
  {#if kind === "password"}
    <div>
      <label for="totp" class="block text-sm font-medium text-foreground mb-2">
        TOTP <span class="text-muted-foreground">{tr("ui.credentialForm.optional")}</span>
      </label>
      <input
        id="totp"
        type="text"
        bind:value={totp}
        placeholder={tr("ui.credentialForm.otpauthTotpOrBase32Secret")}
        class="w-full px-4 py-3 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:outline-none font-mono"
      />
    </div>
  {/if}

  <!-- URL (optional) -->
  <div>
    <label for="url" class="block text-sm font-medium text-foreground mb-2">
      {getUrlLabel()}
      {#if isUrlRequired()}
        <span class="text-destructive">*</span>
      {:else}
        <span class="text-muted-foreground">{tr("ui.credentialForm.optional")}</span>
      {/if}
    </label>
    <input
      id="url"
      type="url"
      bind:value={url}
      placeholder={getUrlPlaceholder()}
      class="w-full px-4 py-3 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:outline-none"
    />
  </div>

  <!-- Expiry (optional) -->
  <div>
    <label for="expires" class="block text-sm font-medium text-foreground mb-2">
      {tr("ui.credentialForm.expiryDate")} <span class="text-muted-foreground">{tr("ui.credentialForm.optional")}</span>
    </label>
    <input
      id="expires"
      type="date"
      bind:value={expiresAt}
      class="w-full px-4 py-3 bg-input border border-border rounded-lg text-foreground focus:border-primary focus:outline-none"
    />
  </div>

  <!-- Notes (optional) -->
  <div>
    <label for="notes" class="block text-sm font-medium text-foreground mb-2">
      {tr("ui.credentialForm.notes")} <span class="text-muted-foreground">{tr("ui.credentialForm.optional")}</span>
    </label>
    <textarea
      id="notes"
      bind:value={notes}
      rows="3"
      placeholder={getNotesPlaceholder()}
      class="w-full px-4 py-3 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:outline-none"
    ></textarea>
  </div>

  <!-- Actions -->
  <div class="flex justify-end gap-3 pt-4 border-t border-border">
    <button
      type="button"
      onclick={onCancel}
      disabled={saving}
      data-kx="control"
      class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
    >
      {tr("common.cancel")}
    </button>
    <button
      type="submit"
      disabled={saving}
      data-kx="control"
      data-variant="primary"
      class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
    >
      {#if saving}
        <span class="flex items-center gap-2">
          <svg class="animate-spin h-4 w-4" viewBox="0 0 24 24">
            <circle
              class="opacity-25"
              cx="12"
              cy="12"
              r="10"
              stroke="currentColor"
              stroke-width="4"
              fill="none"
            />
            <path
              class="opacity-75"
              fill="currentColor"
              d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
            />
          </svg>
          {tr("ui.credentialForm.saving")}
        </span>
      {:else}
        {tr("common.save")}
      {/if}
    </button>
  </div>
</form>
