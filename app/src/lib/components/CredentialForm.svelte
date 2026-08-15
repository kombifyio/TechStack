<script lang="ts">
  import type {
    CredentialType,
    PBWalletItem,
    WalletEntryArea,
  } from "$lib/stores/wallet";
  import { parseApiError } from "$lib/api/errors";
  import { buildWalletEntryPayload } from "$lib/wallet/payload";

  // Props
  interface Props {
    credential?: Partial<PBWalletItem>;
    mode?: WalletEntryArea;
    onSave: (data: Partial<PBWalletItem>) => Promise<void>;
    onCancel: () => void;
    saving?: boolean;
  }

  let { credential, mode, onSave, onCancel, saving = false }: Props = $props();

  function inferWalletArea(item?: Partial<PBWalletItem>): WalletEntryArea {
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
  }> = [
    {
      value: "tools",
      label: "Tools",
      description: "Administrative tools and operational surfaces.",
    },
    {
      value: "access",
      label: "Access",
      description: "Users, portals, IP/device gating, and access controls.",
    },
    {
      value: "recovery",
      label: "Recovery",
      description: "Break-glass secrets and reveal-only recovery material.",
    },
  ];

  const credentialTypes: Array<{
    value: CredentialType;
    label: string;
    abbr: string;
  }> = [
    { value: "password", label: "Password", abbr: "PW" },
    { value: "api_key", label: "API Key", abbr: "API" },
    { value: "ssh_key", label: "SSH Key", abbr: "SSH" },
    { value: "oauth_token", label: "OAuth Token", abbr: "OAuth" },
    { value: "certificate", label: "Certificate", abbr: "Cert" },
    { value: "other", label: "Other", abbr: "?" },
  ];

  function getSecretLabel(): string {
    switch (kind) {
      case "password":
        return "Password";
      case "api_key":
        return "API Key";
      case "ssh_key":
        return "Private Key";
      case "oauth_token":
        return "Token";
      case "certificate":
        return "Certificate Content";
      default:
        return "Secret";
    }
  }

  function getSecretPlaceholder(): string {
    switch (kind) {
      case "password":
        return "Enter password...";
      case "api_key":
        return "sk_live_xxxxx...";
      case "ssh_key":
        return "-----BEGIN OPENSSH PRIVATE KEY-----\n...";
      case "oauth_token":
        return "Bearer token or refresh token...";
      case "certificate":
        return "-----BEGIN CERTIFICATE-----\n...";
      default:
        return "Enter secret value...";
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
        return "e.g., PocketBase Admin";
      case "access":
        return "e.g., Tailscale Device Approval";
      default:
        return "e.g., Break-Glass Envelope";
    }
  }

  function getUsernameLabel(): string {
    switch (walletArea) {
      case "tools":
        return kind === "api_key" ? "API Label / Owner" : "Operator / Account";
      case "access":
        return "User / Identity";
      default:
        return kind === "api_key"
          ? "Key Identifier / Label"
          : "Username / Email";
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
        return "Tool URL";
      case "access":
        return "Portal / Policy URL";
      default:
        return "Recovery URL / Endpoint";
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
        return "What this tool is for, who uses it, and any launch context...";
      case "access":
        return "Describe the user, gate, or access policy this entry belongs to...";
      default:
        return "Describe the recovery path, storage location, or operator instructions...";
    }
  }

  async function handleSubmit(e: Event) {
    e.preventDefault();
    error = null;

    if (!name.trim()) {
      error = "Name is required";
      return;
    }

    if (isUrlRequired() && !url.trim()) {
      error = "Tool URL is required";
      return;
    }

    if (
      walletArea === "access" &&
      !username.trim() &&
      !url.trim() &&
      !secret.trim() &&
      !totp.trim()
    ) {
      error = "Access entries need at least a user, URL, or secret";
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
          stack_id: credential?.stack_id,
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
          (err instanceof Error ? err.message : "Failed to save credential");
      }
    }
  }
</script>

<form onsubmit={handleSubmit} class="space-y-6">
  {#if error}
    <div
      class="p-4 rounded-lg bg-red-900/30 border border-red-500/50 text-red-300"
    >
      {error}
    </div>
  {/if}

  <!-- Wallet Area -->
  <div>
    <span class="block text-sm font-medium text-gray-300 mb-2">
      Wallet Area <span class="text-red-400">*</span>
    </span>
    <div class="grid grid-cols-1 md:grid-cols-3 gap-2">
      {#each walletAreas as area}
        <button
          type="button"
          onclick={() => (walletArea = area.value)}
          class="p-3 rounded-lg border text-left transition-colors {walletArea ===
          area.value
            ? 'bg-primary/10 border-primary/50 text-primary'
            : 'bg-gray-900 border-gray-700 text-gray-400 hover:border-gray-600'}"
        >
          <div class="text-sm font-medium">{area.label}</div>
          <div class="text-xs mt-1 text-current/80">{area.description}</div>
        </button>
      {/each}
    </div>
  </div>

  <!-- Name -->
  <div>
    <label for="name" class="block text-sm font-medium text-gray-300 mb-2">
      Name <span class="text-red-400">*</span>
    </label>
    <input
      id="name"
      type="text"
      bind:value={name}
      placeholder={getNamePlaceholder()}
      class="w-full px-4 py-3 bg-gray-900 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:border-primary focus:outline-none"
    />
  </div>

  <!-- Type -->
  <div>
    <span
      id="credential-type-label"
      class="block text-sm font-medium text-gray-300 mb-2"
    >
      Credential Type <span class="text-red-400">*</span>
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
            : 'bg-gray-900 border-gray-700 text-gray-400 hover:border-gray-600'}"
        >
          <span class="text-xs font-mono mr-2 px-1.5 py-0.5 rounded bg-gray-800"
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
        class="block text-sm font-medium text-gray-300 mb-2"
      >
        {getUsernameLabel()}
      </label>
      <input
        id="username"
        type="text"
        bind:value={username}
        placeholder={getUsernamePlaceholder()}
        class="w-full px-4 py-3 bg-gray-900 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:border-primary focus:outline-none"
      />
    </div>
  {/if}

  <!-- Secret -->
  <div>
    <label for="secret" class="block text-sm font-medium text-gray-300 mb-2">
      {getSecretLabel()}
      {#if isSecretRequired()}
        <span class="text-red-400">*</span>
      {:else}
        <span class="text-gray-500">(optional)</span>
      {/if}
    </label>
    {#if isMultilineSecret()}
      <textarea
        id="secret"
        bind:value={secret}
        rows="6"
        placeholder={getSecretPlaceholder()}
        class="w-full px-4 py-3 bg-gray-900 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:border-primary focus:outline-none font-mono text-sm"
      ></textarea>
    {:else}
      <input
        id="secret"
        type="password"
        bind:value={secret}
        placeholder={getSecretPlaceholder()}
        class="w-full px-4 py-3 bg-gray-900 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:border-primary focus:outline-none font-mono"
      />
    {/if}
  </div>

  <!-- TOTP (optional) -->
  {#if kind === "password"}
    <div>
      <label for="totp" class="block text-sm font-medium text-gray-300 mb-2">
        TOTP <span class="text-gray-500">(optional)</span>
      </label>
      <input
        id="totp"
        type="text"
        bind:value={totp}
        placeholder="otpauth://totp/... or base32 secret"
        class="w-full px-4 py-3 bg-gray-900 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:border-primary focus:outline-none font-mono"
      />
    </div>
  {/if}

  <!-- URL (optional) -->
  <div>
    <label for="url" class="block text-sm font-medium text-gray-300 mb-2">
      {getUrlLabel()}
      {#if isUrlRequired()}
        <span class="text-red-400">*</span>
      {:else}
        <span class="text-gray-500">(optional)</span>
      {/if}
    </label>
    <input
      id="url"
      type="url"
      bind:value={url}
      placeholder={getUrlPlaceholder()}
      class="w-full px-4 py-3 bg-gray-900 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:border-primary focus:outline-none"
    />
  </div>

  <!-- Expiry (optional) -->
  <div>
    <label for="expires" class="block text-sm font-medium text-gray-300 mb-2">
      Expiry Date <span class="text-gray-500">(optional)</span>
    </label>
    <input
      id="expires"
      type="date"
      bind:value={expiresAt}
      class="w-full px-4 py-3 bg-gray-900 border border-gray-700 rounded-lg text-white focus:border-primary focus:outline-none"
    />
  </div>

  <!-- Notes (optional) -->
  <div>
    <label for="notes" class="block text-sm font-medium text-gray-300 mb-2">
      Notes <span class="text-gray-500">(optional)</span>
    </label>
    <textarea
      id="notes"
      bind:value={notes}
      rows="3"
      placeholder={getNotesPlaceholder()}
      class="w-full px-4 py-3 bg-gray-900 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:border-primary focus:outline-none"
    ></textarea>
  </div>

  <!-- Actions -->
  <div class="flex justify-end gap-3 pt-4 border-t border-gray-700">
    <button
      type="button"
      onclick={onCancel}
      disabled={saving}
      class="px-4 py-2 text-sm bg-gray-700 hover:bg-gray-600 disabled:bg-gray-800 text-white rounded-lg transition-colors"
    >
      Cancel
    </button>
    <button
      type="submit"
      disabled={saving}
      class="px-4 py-2 text-sm bg-primary hover:bg-primary/90 disabled:bg-primary/50 disabled:cursor-not-allowed text-white font-medium rounded-lg transition-colors"
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
          Saving...
        </span>
      {:else}
        Save
      {/if}
    </button>
  </div>
</form>
