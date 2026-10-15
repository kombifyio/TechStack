<script lang="ts">
  /**
   * Choose the Wallet item that gives Techstack access to a server: a "server
   * access" item (host, user and an SSH private key or a password). Choosing
   * one is the owner's decision to let Techstack use it, so the `use` right is
   * granted on selection; the page says so. The credential itself never passes
   * through Techstack: it is created in this browser and sealed to the Wallet.
   *
   * An item has to name the server it is used for and hold the credential of
   * the chosen sign-in method; Techstack refuses anything else. Items this
   * editor made say which they are, so the list offers those that fit, and an
   * item that says nothing stays selectable (the backend decides).
   */
  import { onMount } from "svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import Button from "#lib/components/ui/Button.svelte";
  import { nodeAccessFacts, sameHost } from "../item-types.js";
  import {
    allowTechstackToUse,
    listItems,
    listServiceAccess,
    ownerSubject,
    type WalletItem,
  } from "../wallet-api.js";
  import { techstackMayUse } from "../service-access.js";
  import { walletErrorMessage } from "../wallet-text.js";
  import WalletItemDialog, {
    type WalletItemDialogResult,
  } from "./WalletItemDialog.svelte";
  import "./wallet.css";

  interface Props {
    /** The selected item id, or "" when none is chosen. */
    value: string;
    onchange: (itemId: string, name?: string) => void;
    id: string;
    disabled?: boolean;
    /** The server the access is for: items for other hosts are not offered. */
    host?: string;
    /** The chosen sign-in method: items for the other method are not offered. */
    method?: "ssh-key" | "password";
    /**
     * Offer to create the item here, with the connection details the wizard
     * already holds. Omitted where only existing items can be picked.
     */
    createWith?: { host: string; user: string; port: number };
    /** Label for the select (defaults to the generic server-access wording). */
    label?: string;
  }

  let {
    value,
    onchange,
    id,
    disabled = false,
    host = "",
    method,
    createWith,
    label,
  }: Props = $props();

  let items = $state<WalletItem[]>([]);
  let loadState = $state<"loading" | "ready" | "error">("loading");
  let message = $state<string | null>(null);
  let owner = $state<string | null>(null);
  let creating = $state(false);
  let allowing = $state(false);
  let allowed = $state(false);

  const accessItems = $derived(
    items.filter(
      (item) =>
        item.itemType === "node_access_profile" && item.state === "active",
    ),
  );

  /** Whether an item fits the server and method: true, false, or null when it does not say. */
  function fits(item: WalletItem): boolean | null {
    const facts = nodeAccessFacts(item);
    if (!facts) return null;
    if (host.trim() && !sameHost(facts.host, host)) return false;
    if (method && (method === "password") !== (facts.method === "password"))
      return false;
    return true;
  }

  const offered = $derived(accessItems.filter((item) => fits(item) !== false));
  const hidden = $derived(accessItems.length - offered.length);

  async function load() {
    loadState = "loading";
    message = null;
    try {
      const [loaded, subject] = await Promise.all([
        listItems(),
        ownerSubject().catch(() => null),
      ]);
      items = loaded;
      owner = subject;
      loadState = "ready";
    } catch (error) {
      message = walletErrorMessage(error);
      loadState = "error";
    }
  }

  onMount(() => void load());

  // An item that no longer fits (the host or method was changed) is let go;
  // when exactly one item fits and Techstack may already use it, it is chosen.
  let preselecting = 0;
  $effect(() => {
    if (loadState !== "ready") return;
    const selected = accessItems.find((item) => item.itemId === value);
    if (selected && fits(selected) === false) {
      onchange("");
      return;
    }
    if (value || !host.trim()) return;
    const matching = accessItems.filter((item) => fits(item) === true);
    if (matching.length !== 1) return;
    const [only] = matching;
    const attempt = ++preselecting;
    void listServiceAccess(only.itemId)
      .then((grants) => {
        if (attempt !== preselecting || value || !techstackMayUse(grants))
          return;
        allowed = true;
        onchange(only.itemId, only.displayName);
      })
      .catch(() => {});
  });

  async function choose(itemId: string) {
    preselecting += 1;
    allowed = false;
    message = null;
    if (!itemId) {
      onchange("");
      return;
    }
    allowing = true;
    try {
      await allowTechstackToUse(itemId);
      allowed = true;
      onchange(
        itemId,
        items.find((item) => item.itemId === itemId)?.displayName,
      );
    } catch (error) {
      message = walletErrorMessage(error);
      onchange("");
    } finally {
      allowing = false;
    }
  }

  async function created(result: WalletItemDialogResult) {
    creating = false;
    await load();
    await choose(result.itemId);
  }

  const canCreate = $derived(
    Boolean(createWith?.host.trim()) && Boolean(owner) && !disabled,
  );
</script>

<div class="kw-stack" data-testid="wallet-server-access-picker">
  <div>
    <label class="kw-label" for={id}
      >{label ?? tr("kwallet.picker.label")}</label
    >
    <select
      {id}
      class="kw-input"
      data-testid="wallet-access-select"
      disabled={disabled || loadState !== "ready" || allowing}
      {value}
      onchange={(event) => void choose(event.currentTarget.value)}
    >
      <option value="">{tr("kwallet.picker.placeholder")}</option>
      {#each offered as item (item.itemId)}
        <option value={item.itemId}>
          {item.displayName}{fits(item) === null && host.trim()
            ? ` ${tr("kwallet.picker.host_unknown")}`
            : ""}
        </option>
      {/each}
    </select>
    {#if loadState === "loading"}
      <p class="kw-hint" role="status">{tr("kwallet.loading")}</p>
    {:else if loadState === "ready" && offered.length === 0}
      <p class="kw-hint" data-testid="wallet-picker-empty">
        {tr(hidden > 0 ? "kwallet.picker.none_fit" : "kwallet.picker.empty")}
        <a class="text-primary underline" href="/wallet"
          >{tr("kwallet.picker.open_wallet")}</a
        >
      </p>
    {:else if loadState === "ready" && hidden > 0}
      <p class="kw-hint" data-testid="wallet-picker-hidden">
        {tr("kwallet.picker.hidden", { count: String(hidden) })}
      </p>
    {/if}
  </div>

  {#if createWith}
    <div>
      <Button
        variant="secondary"
        size="sm"
        disabled={!canCreate}
        testId="wallet-picker-create"
        onclick={() => (creating = true)}
      >
        {tr("kwallet.picker.create")}
      </Button>
      {#if !createWith.host.trim()}
        <p class="kw-hint">{tr("kwallet.picker.host_first")}</p>
      {/if}
    </div>
  {/if}

  {#if allowed && value}
    <p class="kw-notice" data-tone="ok" data-testid="wallet-techstack-notice">
      {tr("kwallet.picker.techstack_notice")}
    </p>
  {/if}
  {#if message}
    <p role="alert" class="kw-notice" data-tone="error">{message}</p>
    {#if loadState === "error"}
      <Button variant="secondary" size="sm" onclick={() => void load()}
        >{tr("kwallet.retry")}</Button
      >
    {/if}
  {/if}
</div>

{#if creating && owner && createWith}
  <WalletItemDialog
    mode="create"
    ownerSubject={owner}
    allowedTypes={["node_access_profile"]}
    initialType="node_access_profile"
    initialName={createWith.host.trim()}
    initialDraft={{
      host: createWith.host.trim(),
      user: createWith.user.trim() || "root",
      port:
        createWith.port && createWith.port !== 22
          ? String(createWith.port)
          : "",
      nodeAuth: method === "password" ? "password" : "privateKey",
    }}
    lockedFields={["host", "user", "port"]}
    lockAuth={Boolean(method)}
    offerTechstackAccess={false}
    onclose={() => (creating = false)}
    ondone={created}
    onerror={(text) => (message = text)}
  />
{/if}
