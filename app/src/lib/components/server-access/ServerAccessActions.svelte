<script lang="ts">
  import { tr, stateLabel } from "#lib/i18n.svelte.js";
  import { Clipboard, TerminalSquare } from "@lucide/svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import {
    authorizeServerSSHKey,
    getServerAccess,
    type ServerAccess,
  } from "#lib/api/server-access.js";
  import { getWalletItems, type WalletItem } from "#lib/api/wallet.js";
  import { denialMessage, parseApiError } from "#lib/api/errors.js";
  import { confirmInApp } from "#lib/dialogs/in-app-dialog.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import WalletServerAccessPicker from "#lib/kombify-wallet/components/WalletServerAccessPicker.svelte";
  import { isEmbeddedWindow } from "#lib/auth/embedded-session.js";
  import {
    isStepUpRefusal,
    redirectToStepUp,
    SERVER_ACCESS_STEP_UP_REASON,
    serverStepUp,
  } from "#lib/auth/step-up.js";
  import ServerTerminalModal from "./ServerTerminalModal.svelte";
  import StepUpPrompt from "./StepUpPrompt.svelte";

  interface Props {
    serverId: string;
    serverName: string;
    unavailable?: boolean;
    unavailableReason?: string;
    /** "stack" lists the actions full width, for a menu. */
    layout?: "row" | "stack";
  }

  let {
    serverId,
    serverName,
    unavailable = false,
    unavailableReason,
    layout = "row",
  }: Props = $props();
  const buttonClass = $derived(layout === "stack" ? "w-full" : "");
  let access = $state<ServerAccess | null>(null);
  let loading = $state(false);
  let error = $state<string | null>(null);
  let copied = $state(false);
  let showTerminal = $state(false);
  let showKeySetup = $state(false);
  let walletKeys = $state<WalletItem[]>([]);
  let selectedWalletKey = $state("");
  // The Wallet item picked in hosted mode, for the confirmation text.
  let selectedWalletKeyName = $state("");
  // Embedded only: the key install needs a fresh sign-in confirmation that
  // happens in a separate window (the frame itself must never navigate).
  let keyStepUp = $state(false);
  // Hosted Techstack picks a server access item of the person's kombify Wallet
  // instead of a key of the built-in one.
  const useWallet = $derived(authStore.walletSource === "kombify");

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
        (resolved?.reason ? stateLabel(resolved.reason) : "") ||
        tr("ui.serverAccessActions.terminalAccessIsUnavailable");
      return;
    }
    showTerminal = true;
  }

  async function copySSH() {
    const resolved = await resolveAccess();
    if (!resolved?.ssh_command) {
      error =
        (resolved?.reason ? stateLabel(resolved.reason) : "") ||
        tr("ui.serverAccessActions.sshCommandIsUnavailable");
      return;
    }
    if (!resolved.connect_ready) {
      error = tr("ui.serverAccessActions.authorizeOneOfYourPublic");
      await loadWalletKeys();
      showKeySetup = true;
      return;
    }
    await navigator.clipboard.writeText(resolved.ssh_command);
    copied = true;
    window.setTimeout(() => (copied = false), 2000);
  }

  async function loadWalletKeys() {
    if (useWallet || walletKeys.length > 0) return;
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
        title: tr("ui.serverAccessActions.authorizeSshKey"),
        message: tr("ui.serverAccess.installKeyConfirm", {
          key:
            key?.name || selectedWalletKeyName || tr("ui.serverAccess.sshKey"),
          server: serverName,
        }),
        confirmText: tr("ui.serverAccessActions.installPublicKey"),
      }))
    )
      return;
    await performKeyInstall();
  }

  /** Runs the confirmed key install; the step-up retry calls it again. */
  async function performKeyInstall() {
    loading = true;
    error = null;
    try {
      access = await authorizeServerSSHKey(serverId, selectedWalletKey);
      showKeySetup = false;
      keyStepUp = false;
    } catch (cause) {
      const parsed = parseApiError(cause);
      const stepUp = isStepUpRefusal(parsed, SERVER_ACCESS_STEP_UP_REASON);
      if (stepUp && isEmbeddedWindow()) {
        keyStepUp = true;
      } else {
        error = denialMessage(parsed);
        if (stepUp) redirectToStepUp(serverStepUp(serverId));
      }
    } finally {
      loading = false;
    }
  }
</script>

<div
  class={layout === "stack"
    ? "flex flex-col items-stretch gap-2"
    : "flex flex-wrap items-center gap-2"}
  data-testid="server-access-actions"
>
  <Button
    variant="secondary"
    size="sm"
    disabled={unavailable || loading}
    class={buttonClass}
    onclick={() => void openTerminal()}
  >
    <TerminalSquare class="h-4 w-4" /> {tr("ui.serverAccessActions.openTerminal")}
  </Button>
  <Button
    variant="secondary"
    size="sm"
    disabled={unavailable || loading}
    class={buttonClass}
    onclick={() => void copySSH()}
  >
    <Clipboard class="h-4 w-4" />
    {copied ? "Copied" : tr("ui.serverAccessActions.copySshCommand")}
  </Button>
  {#if (unavailable && unavailableReason) || error || (access && !access.connect_ready)}
    <span
      class="basis-full text-xs text-muted-foreground"
      role={error ? "alert" : "status"}
    >
      {error ||
        (unavailable ? unavailableReason : undefined) ||
        tr("ui.serverAccessActions.thisCommandRequiresOneOf")}
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
      {tr("ui.serverAccess.authorizeKey")}
    </Button>
  {/if}
  {#if showKeySetup}
    <div class="basis-full rounded-md border border-border bg-muted/30 p-3">
      {#if keyStepUp}
        <div class="text-foreground">
          <StepUpPrompt
            request={serverStepUp(serverId)}
            body={tr("ui.serverAccessActions.stepUpBody")}
            onRetry={performKeyInstall}
          />
        </div>
      {:else if useWallet}
        <div class="flex flex-wrap items-end gap-2">
          <div class="min-w-52">
            <WalletServerAccessPicker
              id={`ssh-wallet-item-${serverId}`}
              value={selectedWalletKey}
              onchange={(itemId, name) => {
                selectedWalletKey = itemId;
                selectedWalletKeyName = name ?? "";
              }}
              disabled={loading}
            />
          </div>
          <Button
            variant="primary"
            size="sm"
            disabled={loading || !selectedWalletKey}
            testId="server-access-install-key"
            onclick={() => void installSelectedKey()}
            >{tr("ui.serverAccessActions.installPublicKey")}</Button
          >
        </div>
      {:else if walletKeys.length > 0}
        <label
          class="block text-xs font-medium text-foreground"
          for={`ssh-wallet-key-${serverId}`}>{tr("ui.serverAccessActions.walletPublicKey")}</label
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
            >{tr("ui.serverAccessActions.installPublicKey")}</Button
          >
        </div>
      {:else}
        <p class="text-xs text-muted-foreground">
          {tr("ui.serverAccessActions.noSshKeyInThe")} <a
            class="text-primary underline"
            href="/wallet">{tr("ui.serverAccessActions.addOneInTheWallet")}</a
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
  />
{/if}
