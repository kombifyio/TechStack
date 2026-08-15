<script lang="ts">
  import { goto } from "$app/navigation";
  import Modal from "./Modal.svelte";
  import {
    importKombinationSpec,
    exportKombinationSpec,
    validateKombinationImport,
    type ImportValidationResult,
  } from "$lib/api/stacks";

  interface Props {
    mode: "import" | "export";
    stackId?: string;
    onClose: () => void;
    onSuccess?: (result: { stackId: string; jobId: string }) => void;
  }

  let { mode, stackId, onClose, onSuccess }: Props = $props();

  // Import state
  let importFile = $state<File | null>(null);
  let importContent = $state("");
  let importing = $state(false);
  let importError = $state<string | null>(null);
  let importDiagnostics = $state<string | null>(null);
  let validationResult = $state<ImportValidationResult | null>(null);
  let validating = $state(false);
  let importStep = $state<"upload" | "preview" | "importing">("upload");

  // Export state
  let exporting = $state(false);
  let exportError = $state<string | null>(null);
  let exportDiagnostics = $state<string | null>(null);
  let exportFormat = $state<"yaml" | "json">("yaml");

  function buildDiagnostics(action: string, err: unknown) {
    const e = err instanceof Error ? err : null;
    const anyErr = err as any;
    const payload = {
      app: "techstack-ui",
      action,
      mode,
      stackId,
      exportFormat,
      time: new Date().toISOString(),
      url: typeof window !== "undefined" ? window.location.href : "(no-window)",
      userAgent:
        typeof navigator !== "undefined" ? navigator.userAgent : "(no-ua)",
      error: {
        name: e?.name || typeof err,
        message: e?.message || String(err),
        status: typeof anyErr?.status === "number" ? anyErr.status : undefined,
        method: typeof anyErr?.method === "string" ? anyErr.method : undefined,
        requestUrl: typeof anyErr?.url === "string" ? anyErr.url : undefined,
        requestId:
          typeof anyErr?.requestId === "string" ? anyErr.requestId : undefined,
        contentType:
          typeof anyErr?.contentType === "string"
            ? anyErr.contentType
            : undefined,
        responseBodySnippet:
          typeof anyErr?.responseBodySnippet === "string"
            ? anyErr.responseBodySnippet
            : undefined,
        details: anyErr?.details,
      },
    };
    return JSON.stringify(payload, null, 2);
  }

  async function copyText(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Fallback
      try {
        const ta = document.createElement("textarea");
        ta.value = text;
        ta.style.position = "fixed";
        ta.style.left = "-9999px";
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
        return true;
      } catch {
        return false;
      }
    }
  }

  // File input handling
  async function handleFileSelect(e: Event) {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    if (!file) return;

    importFile = file;
    importError = null;
    importDiagnostics = null;
    validationResult = null;

    try {
      const content = await file.text();
      importContent = content;
      await validateContent();
    } catch (err) {
      importError = err instanceof Error ? err.message : "Could not read file";
    }
  }

  // Drag and drop handling
  function handleDragOver(e: DragEvent) {
    e.preventDefault();
    e.dataTransfer!.dropEffect = "copy";
  }

  async function handleDrop(e: DragEvent) {
    e.preventDefault();
    const file = e.dataTransfer?.files?.[0];
    if (!file) return;

    importFile = file;
    importError = null;
    importDiagnostics = null;
    validationResult = null;

    try {
      const content = await file.text();
      importContent = content;
      await validateContent();
    } catch (err) {
      importError = err instanceof Error ? err.message : "Could not read file";
    }
  }

  // Validate the content before import
  async function validateContent() {
    if (!importContent.trim()) {
      importError = "No content found";
      return;
    }

    validating = true;
    importError = null;
    importDiagnostics = null;

    try {
      validationResult = await validateKombinationImport(importContent);
      if (validationResult.valid) {
        importStep = "preview";
      }
    } catch (err) {
      importError = err instanceof Error ? err.message : "Validation failed";
      importDiagnostics = buildDiagnostics("validate-import", err);
    } finally {
      validating = false;
    }
  }

  // Perform the import
  async function handleImport() {
    if (!importContent.trim()) {
      importError = "No content to import";
      return;
    }

    importing = true;
    importError = null;
    importDiagnostics = null;
    importStep = "importing";

    try {
      const result = await importKombinationSpec(importContent);

      // Call success callback or redirect
      if (onSuccess) {
        onSuccess({ stackId: result.stack_id, jobId: result.job_id });
      }

      // Redirect to creating page (same as wizard)
      if (result.redirect) {
        goto(result.redirect);
      } else {
        goto(
          `/stacks/creating?job_id=${result.job_id}&stack_id=${result.stack_id}`,
        );
      }
    } catch (err) {
      importError = err instanceof Error ? err.message : "Import failed";
      importDiagnostics = buildDiagnostics("import", err);
      importStep = "preview";
    } finally {
      importing = false;
    }
  }

  // Handle export
  async function handleExport() {
    if (!stackId) {
      exportError = "No stack ID available";
      return;
    }

    exporting = true;
    exportError = null;
    exportDiagnostics = null;

    try {
      const result = await exportKombinationSpec(stackId, exportFormat);

      // Create download
      const blob = new Blob([result.content], {
        type: exportFormat === "yaml" ? "application/yaml" : "application/json",
      });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `stack-spec.${exportFormat === "yaml" ? "yaml" : "json"}`;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);

      onClose();
    } catch (err) {
      exportError = err instanceof Error ? err.message : "Export failed";
      exportDiagnostics = buildDiagnostics("export", err);
    } finally {
      exporting = false;
    }
  }

  // Reset import state
  function resetImport() {
    importFile = null;
    importContent = "";
    importError = null;
    importDiagnostics = null;
    validationResult = null;
    importStep = "upload";
  }
</script>

<Modal
  title={mode === "import" ? "Import Stack Spec" : "Export Stack Spec"}
  {onClose}
  maxWidth="lg"
>
  {#if mode === "import"}
    <!-- IMPORT MODE -->
    {#if importStep === "upload"}
      <div class="space-y-4">
        <p class="text-gray-400 text-sm">
          Import a <code class="text-primary">stack-spec.yaml</code> file to set
          up your stack. Legacy
          <code class="text-primary">kombination.yaml</code> files are still accepted.
        </p>

        <!-- Drag & Drop Zone -->
        <div
          class="border-2 border-dashed border-gray-700 rounded-lg p-8 text-center hover:border-primary/50 transition-colors cursor-pointer"
          ondragover={handleDragOver}
          ondrop={handleDrop}
          role="button"
          tabindex="0"
        >
          <svg
            class="w-12 h-12 mx-auto text-gray-500 mb-4"
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
          <p class="text-gray-400 mb-2">Drag file here or</p>
          <label class="btn btn-secondary cursor-pointer">
            Select File
            <input
              type="file"
              accept=".yaml,.yml,.json"
              class="hidden"
              onchange={handleFileSelect}
            />
          </label>
          <p class="text-xs text-gray-500 mt-2">
            Supported: .yaml, .yml, .json
          </p>
        </div>

        <!-- Or paste content -->
        <div class="relative">
          <div class="absolute inset-0 flex items-center">
            <div class="w-full border-t border-gray-700"></div>
          </div>
          <div class="relative flex justify-center text-sm">
            <span class="px-2 bg-gray-900 text-gray-500">or paste</span>
          </div>
        </div>

        <textarea
          bind:value={importContent}
          placeholder="# Paste your stack-spec.yaml here..."
          class="w-full h-48 px-4 py-3 bg-gray-900 border border-gray-700 rounded-lg text-white font-mono text-sm placeholder-gray-500 focus:border-primary focus:outline-none resize-none"
          oninput={() => {
            validationResult = null;
            importError = null;
          }}
        ></textarea>

        {#if importContent && !validationResult}
          <button
            onclick={validateContent}
            disabled={validating}
            class="btn btn-primary w-full"
          >
            {#if validating}
              <span class="animate-spin mr-2">⟳</span> Validating...
            {:else}
              Validate Configuration
            {/if}
          </button>
        {/if}
      </div>
    {:else if importStep === "preview"}
      <!-- Preview Step -->
      <div class="space-y-4">
        {#if validationResult}
          <!-- Validation Status -->
          <div
            class="p-4 rounded-lg {validationResult.valid
              ? 'bg-green-900/30 border border-green-700/50'
              : 'bg-red-900/30 border border-red-700/50'}"
          >
            <div class="flex items-center gap-2 mb-2">
              {#if validationResult.valid}
                <svg
                  class="w-5 h-5 text-green-400"
                  fill="none"
                  stroke="currentColor"
                  viewBox="0 0 24 24"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z"
                  />
                </svg>
                <span class="text-green-400 font-medium"
                  >Configuration is valid</span
                >
              {:else}
                <svg
                  class="w-5 h-5 text-red-400"
                  fill="none"
                  stroke="currentColor"
                  viewBox="0 0 24 24"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                  />
                </svg>
                <span class="text-red-400 font-medium"
                  >Configuration errors</span
                >
              {/if}
            </div>

            {#if !validationResult.valid && validationResult.errors?.length}
              <ul class="text-sm text-red-300 space-y-1 mt-2">
                {#each validationResult.errors as error}
                  <li>
                    <code class="text-red-400">{error.path || "root"}</code>: {error.message}
                  </li>
                {/each}
              </ul>
            {/if}

            {#if validationResult.warnings?.length}
              <ul class="text-sm text-yellow-300 space-y-1 mt-2">
                {#each validationResult.warnings as warning}
                  <li>{warning.message}</li>
                {/each}
              </ul>
            {/if}
          </div>

          <!-- Detected Info -->
          {#if validationResult.valid}
            <div class="bg-gray-800/50 rounded-lg p-4">
              <p class="text-gray-300 text-sm">
                All required fields are present. After import, the Unifier
                process will start and determine the appropriate StackKit for
                your configuration.
              </p>
            </div>
          {/if}
        {/if}

        <!-- Action Buttons -->
        <div class="flex gap-3">
          <button onclick={resetImport} class="btn btn-secondary flex-1">
            Back
          </button>
          {#if validationResult?.valid}
            <button
              onclick={handleImport}
              disabled={importing}
              class="btn btn-primary flex-1"
            >
              {#if importing}
                <span
                  class="mr-2 h-4 w-4 animate-spin rounded-full border-2 border-current border-t-transparent"
                  aria-hidden="true"
                ></span> Importing...
              {:else}
                Import & Start Setup
              {/if}
            </button>
          {/if}
        </div>
      </div>
    {:else if importStep === "importing"}
      <!-- Importing State -->
      <div class="text-center py-8">
        <div
          class="mx-auto mb-4 h-8 w-8 animate-spin rounded-full border-2 border-primary border-t-transparent"
        ></div>
        <p class="text-gray-300">Importing configuration...</p>
        <p class="text-gray-500 text-sm mt-2">
          You will be redirected to the setup page shortly.
        </p>
      </div>
    {/if}

    <!-- Error Display -->
    {#if importError}
      <div
        class="mt-4 p-3 rounded-lg bg-red-900/30 border border-red-500/50 text-red-300 text-sm"
      >
        <p class="font-medium">{importError}</p>
        <p class="mt-2 text-xs text-red-200/80">
          Tip: If you see an HTML page instead of JSON, the backend proxy or
          your session/auth may be broken. Please try again.
        </p>

        {#if importDiagnostics}
          <div class="mt-3 flex gap-2">
            <button
              class="btn btn-secondary"
              onclick={async () => {
                if (importDiagnostics) await copyText(importDiagnostics);
              }}
            >
              Copy Diagnostics (for developers)
            </button>
          </div>
        {/if}
      </div>
    {/if}
  {:else}
    <!-- EXPORT MODE -->
    <div class="space-y-4">
      <p class="text-gray-400 text-sm">
        Export your current configuration as <code class="text-primary"
          >stack-spec.yaml</code
        >. You can edit the file and import it again later.
      </p>

      <!-- Format Selection -->
      <div class="flex gap-4">
        <label class="flex items-center gap-2 cursor-pointer">
          <input
            type="radio"
            name="format"
            value="yaml"
            checked={exportFormat === "yaml"}
            onchange={() => (exportFormat = "yaml")}
            class="text-primary"
          />
          <span class="text-white">YAML</span>
          <span class="text-xs text-gray-500">(recommended)</span>
        </label>
        <label class="flex items-center gap-2 cursor-pointer">
          <input
            type="radio"
            name="format"
            value="json"
            checked={exportFormat === "json"}
            onchange={() => (exportFormat = "json")}
            class="text-primary"
          />
          <span class="text-white">JSON</span>
        </label>
      </div>

      <!-- Info Box -->
      <div class="bg-gray-800/50 rounded-lg p-4 text-sm">
        <p class="text-gray-300 mb-2">
          <strong class="text-white">What will be exported?</strong>
        </p>
        <ul class="text-gray-400 space-y-1">
          <li>• Stack name and configuration</li>
          <li>• Node definitions (without credentials)</li>
          <li>• Service configurations</li>
          <li>• System, network, and security settings</li>
          <li>• Metadata and intents</li>
        </ul>
      </div>

      <div
        class="p-3 rounded-lg bg-yellow-900/20 border border-yellow-700/50 text-sm text-yellow-200"
      >
        <strong>Note:</strong> SSH keys and secrets are not exported. These must be
        reconfigured after an import.
      </div>

      {#if exportError}
        <div
          class="p-3 rounded-lg bg-red-900/30 border border-red-500/50 text-red-300 text-sm"
        >
          <p class="font-medium">{exportError}</p>
          <p class="mt-2 text-xs text-red-200/80">
            If the problem persists: copy the diagnostics and share them with
            developers.
          </p>

          {#if exportDiagnostics}
            <div class="mt-3 flex gap-2">
              <button
                class="btn btn-secondary"
                onclick={async () => {
                  if (exportDiagnostics) await copyText(exportDiagnostics);
                }}
              >
                Copy Diagnostics
              </button>
            </div>
          {/if}
        </div>
      {/if}

      <div class="flex gap-3">
        <button onclick={onClose} class="btn btn-secondary flex-1">
          Cancel
        </button>
        <button
          onclick={handleExport}
          disabled={exporting}
          class="btn btn-primary flex-1"
        >
          {#if exporting}
            <span
              class="mr-2 h-4 w-4 animate-spin rounded-full border-2 border-current border-t-transparent"
              aria-hidden="true"
            ></span> Exporting...
          {:else}
            Download
          {/if}
        </button>
      </div>
    </div>
  {/if}
</Modal>

<style>
  code {
    font-family: ui-monospace, monospace;
    font-size: 0.875em;
  }
</style>
