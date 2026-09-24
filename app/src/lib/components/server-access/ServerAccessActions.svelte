<script lang="ts">
  import { Clipboard, TerminalSquare } from "@lucide/svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import {
    authorizeServerSSHKey,
    getServerAccess,
    type ServerAccess,
  } from "#lib/api/server-access.js";
  import { getWalletItems, type WalletItem } from "#lib/api/wallet.js";
  import { parseApiError } from "#lib/api/errors.js";
  import { confirmInApp } from "#lib/dialogs/in-app-dialog.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import ServerTerminalModal from "./ServerTerminalModal.svelte";

  interface Props {
    serverId: string;
    serverName: string;
    unavailable?: boolean;
    unavailableReason?: string;
  }

  let {
    serverId,
    serverName,
    unavailable = false,
    unavailableReason,
  }: Props = $props();
  let access = $state<ServerAccess | null>(null);
  let loading = $state(false);
  let error = $state<string | null>(null);
  let copied = $state(false);
  let showTerminal = $state(false);
  let showKeySetup = $state(false);
  let walletKeys = $state<WalletItem[]>([]);
  let selectedWalletKey = $state("");

  async function resolveAccess(): Promise<ServerAccess | null> {
    if (access) return access;
    loading = true;
    error = null;
    try {
      access = await getServerAccess(serverId);
      return access;
    } catch (cause) {
      error = parseApiError(cause).message;
      return null;
    } finally {
      loading = false;
    }
  }

  async function openTerminal() {
    const resolved = await resolveAccess();
    if (!resolved?.terminal_enabled) {
      error =
        resolved?.reason?.replaceAll("_", " ") ||
        "Terminal access is unavailable";
      return;
    }
    showTerminal = true;
  }

  async function copySSH() {
    const resolved = await resolveAccess();
    if (!resolved?.ssh_command) {
      error =
        resolved?.reason?.replaceAll("_", " ") || "SSH command is unavailable";
      return;
    }
    if (!resolved.connect_ready) {
      error = "Authorize one of your public keys on this Node first.";
      await loadWalletKeys();
      showKeySetup = true;
      return;
    }
    await navigator.clipboard.writeText(resolved.ssh_command);
    copied = true;
    window.setTimeout(() => (copied = false), 2000);
  }

  async function loadWalletKeys() {
    if (walletKeys.length > 0) return;
    loading = true;
    try {
      walletKeys = (await getWalletItems()).filter(
        (item) => item.kind === "ssh_key",
      );
      selectedWalletKey ||= walletKeys[0]?.id || "";
    } catch (cause) {
      error = parseApiError(cause).message;
    } finally {
      loading = false;
    }
  }

  async function installSelectedKey() {
    if (!selectedWalletKey) return;
    const key = walletKeys.find((item) => item.id === selectedWalletKey);
    if (
      !(await confirmInApp({
        title: "Authorize SSH key",
        message: `Install public key "${key?.name || "SSH key"}" once on ${serverName}? The private key never leaves the Wallet.`,
        confirmText: "Install public key",
      }))
    )
      return;
    loading = true;
    error = null;
    try {
      access = await authorizeServerSSHKey(serverId, selectedWalletKey);
      showKeySetup = false;
    } catch (cause) {
      const parsed = parseApiError(cause);
      error = parsed.message;
      if (parsed.isForbidden) requestFreshReauth();
    } finally {
      loading = false;
    }
  }

  function requestFreshReauth() {
    if (typeof window === "undefined") return;
    const returnTo = `${window.location.pathname}${window.location.search}${window.location.hash}`;
    const target = new URL(
      authStore.v2LoginUrl || "/api/v2/auth/login",
      window.location.origin,
    );
    target.searchParams.set("return_to", returnTo);
    target.searchParams.set(
      "reauth_purpose",
      "kombify.cloud.server-terminal.v1",
    );
    target.searchParams.set("reauth_resource", serverId);
    window.location.href = target.toString();
  }
</script>

<div
  class="flex flex-wrap items-center gap-2"
  data-testid="server-access-actions"
>
  <Button
    variant="secondary"
    size="sm"
    disabled={unavailable || loading}
    onclick={() => void openTerminal()}
  >
    <TerminalSquare class="h-4 w-4" /> Open terminal
  </Button>
  <Button
    variant="secondary"
    size="sm"
    disabled={unavailable || loading}
    onclick={() => void copySSH()}
  >
    <Clipboard class="h-4 w-4" />
    {copied ? "Copied" : "Copy SSH command"}
  </Button>
  {#if (unavailable && unavailableReason) || error || (access && !access.connect_ready)}
    <span
      class="basis-full text-xs text-muted-foreground"
      role={error ? "alert" : "status"}
    >
      {error ||
        (unavailable ? unavailableReason : undefined) ||
        "This command requires one of your previously authorized SSH keys."}
    </span>
  {/if}
  {#if access && !access.connect_ready && !unavailable}
    <Button
      variant="secondary"
      size="sm"
      disabled={loading}
      onclick={async () => {
        await loadWalletKeys();
        showKeySetup = true;
      }}
    >
      Authorize my SSH key
    </Button>
  {/if}
  {#if showKeySetup}
    <div class="basis-full rounded-md border border-border bg-muted/30 p-3">
      {#if walletKeys.length > 0}
        <label
          class="block text-xs font-medium text-foreground"
          for={`ssh-wallet-key-${serverId}`}>Wallet Public Key</label
        >
        <div class="mt-2 flex flex-wrap gap-2">
          <select
            id={`ssh-wallet-key-${serverId}`}
            class="min-w-52 rounded-md border border-border bg-background px-3 py-2 text-sm"
            bind:value={selectedWalletKey}
          >
            {#each walletKeys as key (key.id)}
              <option value={key.id}>{key.name}</option>
            {/each}
          </select>
          <Button
            variant="primary"
            size="sm"
            disabled={loading || !selectedWalletKey}
            onclick={() => void installSelectedKey()}
            >Install public key</Button
          >
        </div>
      {:else}
        <p class="text-xs text-muted-foreground">
          No SSH key in the Wallet yet. <a
            class="text-primary underline"
            href="/wallet">Add one in the Wallet</a
          >.
        </p>
      {/if}
    </div>
  {/if}
</div>

{#if showTerminal}
  <ServerTerminalModal
    {serverId}
    {serverName}
    onClose={() => (showTerminal = false)}
    onReauthRequired={requestFreshReauth}
  />
{/if}
