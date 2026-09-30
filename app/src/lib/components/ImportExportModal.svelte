<script lang="ts">
  import { tr, trn, formatDateTime } from "#lib/i18n.svelte.js";
  import {
    exportWallet,
    importWallet,
    downloadExport,
    exportToCSV,
    exportToBitwardenJSON,
    downloadCSV,
    downloadBitwardenJSON,
    readExportFile,
    type WalletExport,
    type ExportableCredential,
  } from "#lib/wallet/export.js";
  import { isCryptoAvailable } from "#lib/wallet/crypto.js";
  import type { WalletItem } from "#lib/wallet/types.js";
  import Modal from "./Modal.svelte";

  interface Props {
    items: WalletItem[];
    onImport: (credentials: ExportableCredential[]) => Promise<void>;
    onClose: () => void;
  }

  let { items, onImport, onClose }: Props = $props();

  // Modal mode
  type Mode = "select" | "export" | "export-format" | "import";
  let mode = $state<Mode>("select");

  // Export format selection
  type ExportFormat = "json" | "csv" | "bitwarden";
  let exportFormat = $state<ExportFormat>("json");

  // Export state
  let exportPassword = $state("");
  let exportConfirmPassword = $state("");
  let exportEncrypted = $state(true);
  let exporting = $state(false);
  let exportError = $state<string | null>(null);

  // Import state
  let importFile = $state<File | null>(null);
  let importPassword = $state("");
  let importPreview = $state<WalletExport | null>(null);
  let importItems = $state<ExportableCredential[]>([]);
  let importing = $state(false);
  let importError = $state<string | null>(null);
  let importStep = $state<"upload" | "password" | "preview">("upload");

  const cryptoAvailable = isCryptoAvailable();

  async function handleExport() {
    exportError = null;

    // Handle CSV export
    if (exportFormat === "csv") {
      try {
        const csvContent = exportToCSV(items);
        downloadCSV(csvContent);
        onClose();
      } catch (err) {
        exportError = err instanceof Error ? err.message : tr("ui.importExportModal.csvExportFailed");
      }
      return;
    }

    // Handle Bitwarden JSON export
    if (exportFormat === "bitwarden") {
      try {
        const bitwardenJSON = exportToBitwardenJSON(items);
        downloadBitwardenJSON(bitwardenJSON);
        onClose();
      } catch (err) {
        exportError =
          err instanceof Error ? err.message : tr("ui.importExportModal.bitwardenExportFailed");
      }
      return;
    }

    // Handle encrypted kombify-TechStack JSON export
    if (exportEncrypted) {
      if (!exportPassword) {
        exportError = tr("ui.importExportModal.passwordIsRequiredForEncrypted");
        return;
      }
      if (exportPassword.length < 8) {
        exportError = tr("ui.importExportModal.passwordMustBeAtLeast");
        return;
      }
      if (exportPassword !== exportConfirmPassword) {
        exportError = tr("ui.importExportModal.passwordsDoNotMatch");
        return;
      }
    }

    exporting = true;
    try {
      const exportData = await exportWallet(
        items,
        exportEncrypted ? exportPassword : undefined,
      );
      downloadExport(exportData);
      onClose();
    } catch (err) {
      exportError = err instanceof Error ? err.message : tr("ui.importExportModal.exportFailed");
    } finally {
      exporting = false;
    }
  }

  async function handleFileSelect(e: Event) {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    if (!file) return;

    importFile = file;
    importError = null;

    try {
      importPreview = await readExportFile(file);

      if (importPreview.encrypted) {
        importStep = "password";
      } else {
        // No password needed, parse directly
        importItems = await importWallet(importPreview);
        importStep = "preview";
      }
    } catch (err) {
      importError = err instanceof Error ? err.message : tr("ui.importExportModal.failedToReadFile");
      importStep = "upload";
    }
  }

  async function handleDecrypt() {
    if (!importPreview) return;
    importError = null;

    if (!importPassword) {
      importError = tr("ui.importExportModal.passwordIsRequired");
      return;
    }

    try {
      importItems = await importWallet(importPreview, importPassword);
      importStep = "preview";
    } catch (err) {
      importError = err instanceof Error ? err.message : tr("ui.importExportModal.decryptionFailed");
    }
  }

  async function handleImport() {
    importing = true;
    importError = null;

    try {
      await onImport(importItems);
      onClose();
    } catch (err) {
      importError = err instanceof Error ? err.message : tr("ui.importExportModal.importFailed");
    } finally {
      importing = false;
    }
  }

  function resetImport() {
    importFile = null;
    importPassword = "";
    importPreview = null;
    importItems = [];
    importError = null;
    importStep = "upload";
  }
</script>

<Modal title={tr("ui.importExportModal.importExportCredentials")} {onClose} maxWidth="lg">
  {#if mode === "select"}
    <div class="space-y-4">
      <button
        onclick={() => (mode = "export")}
        data-kx="plate"
        class="w-full p-4 text-left transition-colors hover:border-primary/50 hover:bg-primary/10"
      >
        <div class="flex items-center gap-3">
          <svg
            class="w-6 h-6 text-primary"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-8l-4-4m0 0L8 8m4-4v12"
            />
          </svg>
          <div>
            <h3 class="text-foreground font-medium">{tr("ui.importExportModal.exportWallet")}</h3>
            <p class="text-sm text-muted-foreground">
              {trn("ui.importExport.downloadEncrypted", items.length)}
            </p>
          </div>
        </div>
      </button>

      <button
        onclick={() => (mode = "import")}
        data-kx="plate"
        class="w-full p-4 text-left transition-colors hover:border-primary/50 hover:bg-primary/10"
      >
        <div class="flex items-center gap-3">
          <svg
            class="w-6 h-6 text-primary"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"
            />
          </svg>
          <div>
            <h3 class="text-foreground font-medium">{tr("ui.importExportModal.importWallet")}</h3>
            <p class="text-sm text-muted-foreground">
              {tr("ui.importExportModal.restoreCredentialsFromABackup")}
            </p>
          </div>
        </div>
      </button>
    </div>

    <div class="mt-6 flex justify-end">
      <button
        onclick={onClose}
        data-kx="control"
        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
      >
        {tr("ui.importExportModal.cancel")}
      </button>
    </div>
  {:else if mode === "export"}
    <div class="flex items-center gap-3 mb-6">
      <button
        onclick={() => (mode = "select")}
        class="text-muted-foreground hover:text-foreground"
      >
        ←
      </button>
      <h2 class="text-xl font-semibold text-foreground">{tr("ui.importExportModal.exportCredentials")}</h2>
    </div>

    {#if exportError}
      <div
        class="mb-4 p-3 rounded-lg bg-destructive/10 border border-destructive/30 text-destructive text-sm"
      >
        {exportError}
      </div>
    {/if}

    <div class="space-y-4">
      <div data-kx="plate" class="p-4 text-sm">
        <p class="text-muted-foreground">
          <strong class="text-foreground">{items.length}</strong>
          {trn("ui.importExport.willBeExported", items.length)}
        </p>
      </div>

      <!-- Export Format Selection -->
      <div>
        <div class="block text-sm font-medium text-foreground mb-3">
          {tr("ui.importExportModal.exportFormat")}
        </div>
        <div class="space-y-2">
          <label
            data-kx="plate"
            class="flex items-start gap-3 p-3 cursor-pointer transition-colors hover:border-primary/50"
          >
            <input
              type="radio"
              bind:group={exportFormat}
              value="json"
              class="mt-1 w-4 h-4 border-border bg-input text-primary focus:ring-primary"
            />
            <div class="flex-1">
              <span class="text-foreground font-medium">{tr("ui.importExportModal.kombifyTechstackJson")}</span>
              <p class="text-xs text-muted-foreground mt-0.5">
                {tr("ui.importExportModal.nativeFormatWithEncryptionSupport")}
              </p>
            </div>
          </label>

          <label
            data-kx="plate"
            class="flex items-start gap-3 p-3 cursor-pointer transition-colors hover:border-primary/50"
          >
            <input
              type="radio"
              bind:group={exportFormat}
              value="csv"
              class="mt-1 w-4 h-4 border-border bg-input text-primary focus:ring-primary"
            />
            <div class="flex-1">
              <span class="text-foreground font-medium">{tr("ui.importExportModal.universalCsv")}</span>
              <p class="text-xs text-muted-foreground mt-0.5">
                {tr("ui.importExportModal.compatibleWithKeepassLastpassAnd")}
              </p>
            </div>
          </label>

          <label
            data-kx="plate"
            class="flex items-start gap-3 p-3 cursor-pointer transition-colors hover:border-primary/50"
          >
            <input
              type="radio"
              bind:group={exportFormat}
              value="bitwarden"
              class="mt-1 w-4 h-4 border-border bg-input text-primary focus:ring-primary"
            />
            <div class="flex-1">
              <span class="text-foreground font-medium">{tr("ui.importExportModal.bitwardenJson")}</span>
              <p class="text-xs text-muted-foreground mt-0.5">
                {tr("ui.importExportModal.importDirectlyIntoBitwardenOr")}
              </p>
            </div>
          </label>
        </div>
      </div>

      {#if exportFormat === "json"}
        <div>
          <label class="flex items-center gap-3 cursor-pointer">
            <input
              type="checkbox"
              bind:checked={exportEncrypted}
              disabled={!cryptoAvailable}
              class="w-5 h-5 rounded border-border bg-input text-primary focus:ring-primary"
            />
            <div>
              <span class="text-foreground">{tr("ui.importExportModal.encryptExport")}</span>
              {#if !cryptoAvailable}
                <p class="text-xs text-warning">
                  {tr("ui.importExportModal.encryptionNotAvailableRequiresHttps")}
                </p>
              {/if}
            </div>
          </label>
        </div>

        {#if exportEncrypted && cryptoAvailable}
          <div>
            <label
              for="export-password"
              class="block text-sm font-medium text-foreground mb-2"
            >
              {tr("ui.importExportModal.encryptionPassword")} <span class="text-destructive">*</span>
            </label>
            <input
              id="export-password"
              type="password"
              bind:value={exportPassword}
              placeholder={tr("ui.importExportModal.min8Characters")}
              class="w-full px-4 py-3 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:outline-none"
            />
          </div>

          <div>
            <label
              for="export-confirm"
              class="block text-sm font-medium text-foreground mb-2"
            >
              {tr("ui.importExportModal.confirmPassword")} <span class="text-destructive">*</span>
            </label>
            <input
              id="export-confirm"
              type="password"
              bind:value={exportConfirmPassword}
              placeholder={tr("ui.importExportModal.reEnterPassword")}
              class="w-full px-4 py-3 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:outline-none"
            />
          </div>

          <div
            class="p-3 rounded-lg bg-warning/10 border border-warning/30 text-sm text-warning"
          >
            <strong>{tr("ui.importExportModal.important")}</strong> {tr("ui.importExportModal.keepThisPasswordSafeWithout")}
          </div>
        {/if}
      {/if}

      {#if exportFormat !== "json"}
        <div
          class="p-3 rounded-lg bg-info/10 border border-info/30 text-sm text-info"
        >
          <strong>{tr("ui.importExportModal.note")}</strong>
          {tr("ui.importExport.notEncrypted", { format: exportFormat === "csv" ? "CSV" : "Bitwarden JSON" })}
        </div>
      {:else if !exportEncrypted}
        <div
          class="p-3 rounded-lg bg-destructive/10 border border-destructive/30 text-sm text-destructive"
        >
          <strong>{tr("ui.importExportModal.warning")}</strong> {tr("ui.importExportModal.exportingWithoutEncryptionWillSave")}
        </div>
      {/if}
    </div>

    <div class="mt-6 flex justify-end gap-3">
      <button
        onclick={() => (mode = "select")}
        data-kx="control"
        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
      >
        {tr("ui.importExportModal.back")}
      </button>
      <button
        onclick={handleExport}
        disabled={exporting || items.length === 0}
        data-kx="control"
        data-variant="primary"
        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
      >
        {#if exporting}
          {tr("ui.importExportModal.exporting")}
        {:else}
          {tr("ui.importExportModal.download")}
        {/if}
      </button>
    </div>
  {:else if mode === "import"}
    <div class="flex items-center gap-3 mb-6">
      <button
        onclick={() => {
          mode = "select";
          resetImport();
        }}
        class="text-muted-foreground hover:text-foreground"
      >
        ←
      </button>
      <h2 class="text-xl font-semibold text-foreground">{tr("ui.importExportModal.importCredentials")}</h2>
    </div>

    {#if importError}
      <div
        class="mb-4 p-3 rounded-lg bg-destructive/10 border border-destructive/30 text-destructive text-sm"
      >
        {importError}
      </div>
    {/if}

    {#if importStep === "upload"}
      <div class="space-y-4">
        <div
          class="border-2 border-dashed border-border rounded-lg p-8 text-center hover:border-muted-foreground transition-colors"
        >
          <input
            type="file"
            accept=".json"
            onchange={handleFileSelect}
            class="hidden"
            id="import-file"
          />
          <label for="import-file" class="cursor-pointer">
            <svg
              class="w-12 h-12 mx-auto mb-3 text-muted-foreground"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M7 16a4 4 0 01-.88-7.903A5 5 0 1115.9 6L16 6a5 5 0 011 9.9M15 13l-3-3m0 0l-3 3m3-3v12"
              />
            </svg>
            <p class="text-foreground mb-1">{tr("ui.importExportModal.clickToSelectFile")}</p>
            <p class="text-sm text-muted-foreground">
              {tr("ui.importExportModal.kombifyTechstackWalletExportJson")}
            </p>
          </label>
        </div>
      </div>
    {:else if importStep === "password"}
      <div class="space-y-4">
        <div
          data-kx="plate" class="p-4 text-sm"
        >
          <p class="text-muted-foreground">
            {tr("ui.importExportModal.thisExportIsEncryptedEnter")}
          </p>
          {#if importPreview}
            <p class="text-muted-foreground mt-1 text-xs">
              {tr("ui.importExport.exportedSummary", { date: formatDateTime(importPreview.exportedAt), count: importPreview.itemCount })}
            </p>
          {/if}
        </div>

        <div>
          <label
            for="import-password"
            class="block text-sm font-medium text-foreground mb-2"
          >
            {tr("ui.importExportModal.decryptionPassword")} <span class="text-destructive">*</span>
          </label>
          <input
            id="import-password"
            type="password"
            bind:value={importPassword}
            placeholder={tr("ui.importExportModal.enterExportPassword")}
            class="w-full px-4 py-3 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:outline-none"
          />
        </div>

        <div class="flex gap-3">
          <button
            onclick={resetImport}
            data-kx="control"
        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
          >
            {tr("ui.importExportModal.chooseDifferentFile")}
          </button>
          <button
            onclick={handleDecrypt}
            data-kx="control"
            data-variant="primary"
            class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
          >
            {tr("ui.importExportModal.decrypt")}
          </button>
        </div>
      </div>
    {:else if importStep === "preview"}
      <div class="space-y-4">
        <div
          data-kx="plate" class="p-4 text-sm"
        >
          <p class="text-foreground font-medium mb-2">
            {trn("ui.importExport.readyToImport", importItems.length)}
          </p>
          <div class="max-h-48 overflow-y-auto space-y-1">
            {#each importItems as item}
              <div class="flex items-center gap-2 text-foreground">
                <span data-kx="tag" class="text-xs font-mono px-1.5 py-0.5">
                  {item.kind === "password"
                    ? "PW"
                    : item.kind === "api_key"
                      ? "API"
                      : item.kind === "ssh_key"
                        ? "SSH"
                        : item.kind === "oauth_token"
                          ? "OAuth"
                          : item.kind === "certificate"
                            ? tr("ui.credentialForm.cert")
                            : "?"}
                </span>
                <span class="truncate">{item.name}</span>
                <span class="text-muted-foreground text-xs">({item.kind})</span>
              </div>
            {/each}
          </div>
        </div>

        <div
          class="p-3 rounded-lg bg-warning/10 border border-warning/30 text-sm text-warning"
        >
          <strong>{tr("ui.importExportModal.note")}</strong> {tr("ui.importExportModal.importedCredentialsWillBeAdded")}
        </div>
      </div>
    {/if}

    <div class="mt-6 flex justify-end gap-3">
      <button
        onclick={() => {
          mode = "select";
          resetImport();
        }}
        data-kx="control"
        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
      >
        {importStep === "upload" ? tr("ui.importExportModal.back") : tr("ui.importExportModal.cancel")}
      </button>
      {#if importStep === "preview"}
        <button
          onclick={handleImport}
          disabled={importing || importItems.length === 0}
          data-kx="control"
        data-variant="primary"
        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
        >
          {#if importing}
            {tr("ui.importExportModal.importing")}
          {:else}
            {tr("ui.importExportModal.import")}
          {/if}
        </button>
      {/if}
    </div>
  {/if}
</Modal>
