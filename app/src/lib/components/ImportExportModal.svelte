<script lang="ts">
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
  } from "$lib/wallet/export";
  import { isCryptoAvailable } from "$lib/wallet/crypto";
  import type { PBWalletItem } from "$lib/stores/wallet";
  import Modal from "./Modal.svelte";

  interface Props {
    items: PBWalletItem[];
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
        exportError = err instanceof Error ? err.message : "CSV export failed";
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
          err instanceof Error ? err.message : "Bitwarden export failed";
      }
      return;
    }

    // Handle encrypted kombify-TechStack JSON export
    if (exportEncrypted) {
      if (!exportPassword) {
        exportError = "Password is required for encrypted export";
        return;
      }
      if (exportPassword.length < 8) {
        exportError = "Password must be at least 8 characters";
        return;
      }
      if (exportPassword !== exportConfirmPassword) {
        exportError = "Passwords do not match";
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
      exportError = err instanceof Error ? err.message : "Export failed";
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
      importError = err instanceof Error ? err.message : "Failed to read file";
      importStep = "upload";
    }
  }

  async function handleDecrypt() {
    if (!importPreview) return;
    importError = null;

    if (!importPassword) {
      importError = "Password is required";
      return;
    }

    try {
      importItems = await importWallet(importPreview, importPassword);
      importStep = "preview";
    } catch (err) {
      importError = err instanceof Error ? err.message : "Decryption failed";
    }
  }

  async function handleImport() {
    importing = true;
    importError = null;

    try {
      await onImport(importItems);
      onClose();
    } catch (err) {
      importError = err instanceof Error ? err.message : "Import failed";
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

<Modal title="Import / Export Credentials" {onClose} maxWidth="lg">
  {#if mode === "select"}
    <div class="space-y-4">
      <button
        onclick={() => (mode = "export")}
        class="w-full p-4 rounded-lg border border-gray-700 bg-gray-800/50 hover:border-primary/50 hover:bg-primary/10 transition-colors text-left"
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
            <h3 class="text-white font-medium">Export Wallet</h3>
            <p class="text-sm text-gray-400">
              Download {items.length} credential{items.length !== 1 ? "s" : ""} as
              encrypted JSON
            </p>
          </div>
        </div>
      </button>

      <button
        onclick={() => (mode = "import")}
        class="w-full p-4 rounded-lg border border-gray-700 bg-gray-800/50 hover:border-primary/50 hover:bg-primary/10 transition-colors text-left"
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
            <h3 class="text-white font-medium">Import Wallet</h3>
            <p class="text-sm text-gray-400">
              Restore credentials from a backup file
            </p>
          </div>
        </div>
      </button>
    </div>

    <div class="mt-6 flex justify-end">
      <button
        onclick={onClose}
        class="px-4 py-2 text-sm bg-gray-700 hover:bg-gray-600 text-white rounded-lg transition-colors"
      >
        Cancel
      </button>
    </div>
  {:else if mode === "export"}
    <div class="flex items-center gap-3 mb-6">
      <button
        onclick={() => (mode = "select")}
        class="text-gray-400 hover:text-white"
      >
        ←
      </button>
      <h2 class="text-xl font-semibold text-white">Export Credentials</h2>
    </div>

    {#if exportError}
      <div
        class="mb-4 p-3 rounded-lg bg-red-900/30 border border-red-500/50 text-red-300 text-sm"
      >
        {exportError}
      </div>
    {/if}

    <div class="space-y-4">
      <div class="p-4 rounded-lg bg-gray-800/50 border border-gray-700 text-sm">
        <p class="text-gray-300">
          <strong class="text-white">{items.length}</strong>
          credential{items.length !== 1 ? "s" : ""} will be exported.
        </p>
      </div>

      <!-- Export Format Selection -->
      <div>
        <div class="block text-sm font-medium text-gray-300 mb-3">
          Export Format
        </div>
        <div class="space-y-2">
          <label
            class="flex items-start gap-3 p-3 rounded-lg border border-gray-700 bg-gray-800/30 hover:border-primary/50 cursor-pointer transition-colors"
          >
            <input
              type="radio"
              bind:group={exportFormat}
              value="json"
              class="mt-1 w-4 h-4 border-gray-600 bg-gray-800 text-primary focus:ring-primary"
            />
            <div class="flex-1">
              <span class="text-white font-medium">kombify-TechStack JSON</span>
              <p class="text-xs text-gray-400 mt-0.5">
                Native format with encryption support. Best for backup and
                restore.
              </p>
            </div>
          </label>

          <label
            class="flex items-start gap-3 p-3 rounded-lg border border-gray-700 bg-gray-800/30 hover:border-primary/50 cursor-pointer transition-colors"
          >
            <input
              type="radio"
              bind:group={exportFormat}
              value="csv"
              class="mt-1 w-4 h-4 border-gray-600 bg-gray-800 text-primary focus:ring-primary"
            />
            <div class="flex-1">
              <span class="text-white font-medium">Universal CSV</span>
              <p class="text-xs text-gray-400 mt-0.5">
                Compatible with KeePass, LastPass, and generic password
                managers.
              </p>
            </div>
          </label>

          <label
            class="flex items-start gap-3 p-3 rounded-lg border border-gray-700 bg-gray-800/30 hover:border-primary/50 cursor-pointer transition-colors"
          >
            <input
              type="radio"
              bind:group={exportFormat}
              value="bitwarden"
              class="mt-1 w-4 h-4 border-gray-600 bg-gray-800 text-primary focus:ring-primary"
            />
            <div class="flex-1">
              <span class="text-white font-medium">Bitwarden JSON</span>
              <p class="text-xs text-gray-400 mt-0.5">
                Import directly into Bitwarden or compatible vaults.
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
              class="w-5 h-5 rounded border-gray-600 bg-gray-800 text-primary focus:ring-primary"
            />
            <div>
              <span class="text-white">Encrypt export</span>
              {#if !cryptoAvailable}
                <p class="text-xs text-yellow-400">
                  Encryption not available (requires HTTPS)
                </p>
              {/if}
            </div>
          </label>
        </div>

        {#if exportEncrypted && cryptoAvailable}
          <div>
            <label
              for="export-password"
              class="block text-sm font-medium text-gray-300 mb-2"
            >
              Encryption Password <span class="text-red-400">*</span>
            </label>
            <input
              id="export-password"
              type="password"
              bind:value={exportPassword}
              placeholder="Min. 8 characters"
              class="w-full px-4 py-3 bg-gray-900 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:border-primary focus:outline-none"
            />
          </div>

          <div>
            <label
              for="export-confirm"
              class="block text-sm font-medium text-gray-300 mb-2"
            >
              Confirm Password <span class="text-red-400">*</span>
            </label>
            <input
              id="export-confirm"
              type="password"
              bind:value={exportConfirmPassword}
              placeholder="Re-enter password"
              class="w-full px-4 py-3 bg-gray-900 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:border-primary focus:outline-none"
            />
          </div>

          <div
            class="p-3 rounded-lg bg-yellow-900/20 border border-yellow-700/50 text-sm text-yellow-200"
          >
            <strong>Important:</strong> Keep this password safe! Without it, you won't
            be able to restore your credentials.
          </div>
        {/if}
      {/if}

      {#if exportFormat !== "json"}
        <div
          class="p-3 rounded-lg bg-blue-900/20 border border-blue-700/50 text-sm text-blue-200"
        >
          <strong>Note:</strong>
          {exportFormat === "csv" ? "CSV" : "Bitwarden JSON"} exports are not encrypted.
          Store the file securely.
        </div>
      {:else if !exportEncrypted}
        <div
          class="p-3 rounded-lg bg-red-900/20 border border-red-700/50 text-sm text-red-200"
        >
          <strong>Warning:</strong> Exporting without encryption will save all secrets
          in plain text. Only use this for testing.
        </div>
      {/if}
    </div>

    <div class="mt-6 flex justify-end gap-3">
      <button
        onclick={() => (mode = "select")}
        class="px-4 py-2 text-sm bg-gray-700 hover:bg-gray-600 text-white rounded-lg transition-colors"
      >
        Back
      </button>
      <button
        onclick={handleExport}
        disabled={exporting || items.length === 0}
        class="px-4 py-2 text-sm bg-primary hover:bg-primary/90 disabled:bg-primary/50 disabled:cursor-not-allowed text-white font-medium rounded-lg transition-colors"
      >
        {#if exporting}
          Exporting...
        {:else}
          Download
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
        class="text-gray-400 hover:text-white"
      >
        ←
      </button>
      <h2 class="text-xl font-semibold text-white">Import Credentials</h2>
    </div>

    {#if importError}
      <div
        class="mb-4 p-3 rounded-lg bg-red-900/30 border border-red-500/50 text-red-300 text-sm"
      >
        {importError}
      </div>
    {/if}

    {#if importStep === "upload"}
      <div class="space-y-4">
        <div
          class="border-2 border-dashed border-gray-600 rounded-lg p-8 text-center hover:border-gray-500 transition-colors"
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
              class="w-12 h-12 mx-auto mb-3 text-gray-500"
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
            <p class="text-white mb-1">Click to select file</p>
            <p class="text-sm text-gray-400">
              kombify-TechStack wallet export (.json)
            </p>
          </label>
        </div>
      </div>
    {:else if importStep === "password"}
      <div class="space-y-4">
        <div
          class="p-4 rounded-lg bg-gray-800/50 border border-gray-700 text-sm"
        >
          <p class="text-gray-300">
            This export is encrypted. Enter the password to decrypt.
          </p>
          {#if importPreview}
            <p class="text-gray-500 mt-1 text-xs">
              Exported: {new Date(importPreview.exportedAt).toLocaleString()}
              • {importPreview.itemCount} items
            </p>
          {/if}
        </div>

        <div>
          <label
            for="import-password"
            class="block text-sm font-medium text-gray-300 mb-2"
          >
            Decryption Password <span class="text-red-400">*</span>
          </label>
          <input
            id="import-password"
            type="password"
            bind:value={importPassword}
            placeholder="Enter export password"
            class="w-full px-4 py-3 bg-gray-900 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:border-primary focus:outline-none"
          />
        </div>

        <div class="flex gap-3">
          <button
            onclick={resetImport}
            class="px-4 py-2 text-sm bg-gray-700 hover:bg-gray-600 text-white rounded-lg transition-colors"
          >
            Choose Different File
          </button>
          <button
            onclick={handleDecrypt}
            class="px-4 py-2 text-sm bg-primary hover:bg-primary/90 text-white font-medium rounded-lg transition-colors"
          >
            Decrypt
          </button>
        </div>
      </div>
    {:else if importStep === "preview"}
      <div class="space-y-4">
        <div
          class="p-4 rounded-lg bg-gray-800/50 border border-gray-700 text-sm"
        >
          <p class="text-white font-medium mb-2">
            Ready to import {importItems.length} credential{importItems.length !==
            1
              ? "s"
              : ""}
          </p>
          <div class="max-h-48 overflow-y-auto space-y-1">
            {#each importItems as item}
              <div class="flex items-center gap-2 text-gray-300">
                <span
                  class="text-xs font-mono px-1.5 py-0.5 rounded bg-gray-800"
                >
                  {item.kind === "password"
                    ? "PW"
                    : item.kind === "api_key"
                      ? "API"
                      : item.kind === "ssh_key"
                        ? "SSH"
                        : item.kind === "oauth_token"
                          ? "OAuth"
                          : item.kind === "certificate"
                            ? "Cert"
                            : "?"}
                </span>
                <span class="truncate">{item.name}</span>
                <span class="text-gray-500 text-xs">({item.kind})</span>
              </div>
            {/each}
          </div>
        </div>

        <div
          class="p-3 rounded-lg bg-yellow-900/20 border border-yellow-700/50 text-sm text-yellow-200"
        >
          <strong>Note:</strong> Imported credentials will be added as new entries.
          Duplicates are not automatically merged.
        </div>
      </div>
    {/if}

    <div class="mt-6 flex justify-end gap-3">
      <button
        onclick={() => {
          mode = "select";
          resetImport();
        }}
        class="px-4 py-2 text-sm bg-gray-700 hover:bg-gray-600 text-white rounded-lg transition-colors"
      >
        {importStep === "upload" ? "Back" : "Cancel"}
      </button>
      {#if importStep === "preview"}
        <button
          onclick={handleImport}
          disabled={importing || importItems.length === 0}
          class="px-4 py-2 text-sm bg-primary hover:bg-primary/90 disabled:bg-primary/50 disabled:cursor-not-allowed text-white font-medium rounded-lg transition-colors"
        >
          {#if importing}
            Importing...
          {:else}
            Import
          {/if}
        </button>
      {/if}
    </div>
  {/if}
</Modal>
