<script lang="ts">
  /**
   * Import credentials from a Techstack export (JSON or CSV) or a Bitwarden
   * JSON export into the kombify Wallet. Every row becomes a Wallet item
   * created from this browser (the value is encrypted here); the outcome is
   * reported per row. The file and the parsed secrets are dropped when the
   * dialog closes. There is no plaintext export from the Wallet: a value
   * leaves it one reveal at a time.
   */
  import { onDestroy } from "svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import Button from "#lib/components/ui/Button.svelte";
  import { importWallet, type WalletExport } from "#lib/wallet/export.js";
  import {
    detectImportFormat,
    type ExportableCredential,
    importableItem,
    parseBitwardenCredentials,
    parseCsvCredentials,
  } from "../import.js";
  import { createItem } from "../wallet-api.js";
  import { itemTypeLabel, walletErrorMessage } from "../wallet-text.js";
  import WalletDialog from "./WalletDialog.svelte";
  import "./wallet.css";

  interface Props {
    ownerSubject: string;
    onclose: () => void;
  }

  let { ownerSubject, onclose }: Props = $props();

  type Row = {
    name: string;
    itemType?: string;
    problem?: "name_invalid" | "no_secret" | "too_large";
    outcome: "pending" | "created" | "failed";
    message?: string;
  };

  let step = $state<"choose" | "password" | "preview" | "done">("choose");
  let error = $state<string | null>(null);
  let password = $state("");
  let rows = $state<Row[]>([]);
  let working = $state(false);
  let pendingExport: WalletExport | null = null;
  let credentials: ExportableCredential[] = [];

  function wipe() {
    for (const credential of credentials) {
      credential.secret = "";
      credential.totp = "";
    }
    credentials = [];
    pendingExport = null;
    password = "";
  }

  onDestroy(wipe);

  function close() {
    wipe();
    onclose();
  }

  function preview(parsed: ExportableCredential[]) {
    credentials = parsed;
    rows = parsed.map((credential) => {
      const item = importableItem(credential);
      return "problem" in item
        ? { name: credential.name, problem: item.problem, outcome: "pending" }
        : {
            name: item.displayName,
            itemType: item.itemType,
            outcome: "pending",
          };
    });
    step = "preview";
  }

  async function onFile(event: Event) {
    const input = event.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = "";
    if (!file) return;
    error = null;
    try {
      const text = await file.text();
      const format = detectImportFormat(text);
      if (format === "csv") preview(parseCsvCredentials(text));
      else if (format === "bitwarden") preview(parseBitwardenCredentials(text));
      else {
        const parsed = JSON.parse(text) as WalletExport;
        if (
          typeof parsed.version !== "number" ||
          typeof parsed.encrypted !== "boolean" ||
          typeof parsed.data !== "string"
        )
          throw new Error("format");
        if (parsed.encrypted) {
          pendingExport = parsed;
          step = "password";
        } else preview(await importWallet(parsed));
      }
    } catch {
      error = tr("kwallet.import.error.unreadable");
    }
  }

  async function decrypt(event: SubmitEvent) {
    event.preventDefault();
    if (!pendingExport) return;
    error = null;
    try {
      preview(await importWallet(pendingExport, password));
      pendingExport = null;
      password = "";
    } catch {
      error = tr("kwallet.import.error.password");
    }
  }

  const importable = $derived(rows.filter((row) => !row.problem).length);

  async function run() {
    if (working) return;
    working = true;
    error = null;
    for (const [index, credential] of credentials.entries()) {
      const item = importableItem(credential);
      if ("problem" in item) continue;
      try {
        await createItem({
          itemType: item.itemType,
          displayName: item.displayName,
          scope: "account",
          ...(item.description ? { description: item.description } : {}),
          value: item.value,
          ownerSubject,
        });
        rows[index].outcome = "created";
      } catch (failure) {
        rows[index].outcome = "failed";
        rows[index].message = walletErrorMessage(failure);
      }
    }
    wipe();
    working = false;
    step = "done";
  }

  const created = $derived(
    rows.filter((row) => row.outcome === "created").length,
  );
  const failed = $derived(
    rows.filter((row) => row.outcome === "failed").length,
  );
</script>

<WalletDialog
  title={tr("kwallet.import.title")}
  description={tr("kwallet.import.description")}
  onclose={close}
>
  <div class="kw-stack" data-testid="wallet-import">
    {#if step === "choose"}
      <div>
        <label class="kw-label" for="kw-import-file"
          >{tr("kwallet.import.file")}</label
        >
        <input
          id="kw-import-file"
          class="kw-input"
          type="file"
          accept=".json,.csv,application/json,text/csv"
          data-testid="wallet-import-file"
          onchange={onFile}
        />
        <p class="kw-hint">{tr("kwallet.import.formats")}</p>
        <p class="kw-hint">{tr("kwallet.import.no_export")}</p>
      </div>
    {:else if step === "password"}
      <form class="kw-stack" onsubmit={decrypt}>
        <div>
          <label class="kw-label" for="kw-import-password"
            >{tr("kwallet.import.password")}</label
          >
          <input
            id="kw-import-password"
            class="kw-input"
            type="password"
            autocomplete="off"
            bind:value={password}
          />
        </div>
        <div class="kw-actions">
          <Button
            type="submit"
            variant="primary"
            size="sm"
            disabled={!password}
          >
            {tr("kwallet.import.decrypt")}
          </Button>
        </div>
      </form>
    {:else}
      <p class="text-sm" data-testid="wallet-import-summary">
        {#if step === "preview"}
          {tr("kwallet.import.preview", {
            count: String(importable),
            total: String(rows.length),
          })}
        {:else}
          {tr("kwallet.import.result", {
            created: String(created),
            failed: String(failed),
          })}
        {/if}
      </p>
      <ul class="kw-stack kw-scroll" data-testid="wallet-import-rows">
        {#each rows as row, index (index)}
          <li class="text-sm">
            <span class="text-foreground font-medium">{row.name || "-"}</span>
            {#if row.itemType}<span class="kw-muted">
                - {itemTypeLabel(row.itemType)}</span
              >{/if}
            <span
              class="kw-notice"
              data-tone={row.outcome === "created"
                ? "ok"
                : row.outcome === "failed" || row.problem
                  ? "error"
                  : "warn"}
            >
              {#if row.outcome === "created"}
                {tr("kwallet.import.row.created")}
              {:else if row.outcome === "failed"}
                {row.message}
              {:else if row.problem}
                {tr(`kwallet.import.row.${row.problem}`)}
              {/if}
            </span>
          </li>
        {/each}
      </ul>
      <div class="kw-actions">
        {#if step === "preview"}
          <Button
            variant="primary"
            size="sm"
            disabled={working || importable === 0}
            testId="wallet-import-run"
            onclick={() => void run()}
          >
            {working
              ? tr("kwallet.working")
              : tr("kwallet.import.run", { count: String(importable) })}
          </Button>
        {:else}
          <Button variant="primary" size="sm" onclick={close}
            >{tr("kwallet.close")}</Button
          >
        {/if}
      </div>
    {/if}
    {#if error}<p role="alert" class="kw-notice" data-tone="error">
        {error}
      </p>{/if}
  </div>
</WalletDialog>
