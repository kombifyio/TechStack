<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { goto } from "$app/navigation";
  import Modal from "./Modal.svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import {
    importKombinationSpec,
    exportKombinationSpec,
    validateKombinationImport,
    type ImportValidationResult,
  } from "#lib/api/stacks.js";

  interface Props {
    mode: "import" | "export";
    kitDeploymentId?: string;
    onClose: () => void;
    onSuccess?: (result: { kitDeploymentId: string; jobId: string }) => void;
  }

  let { mode, kitDeploymentId, onClose, onSuccess }: Props = $props();

  // Import state
  let importFile = $state<File | null>(null);
  let importContent = $state("");
  let importing = $state(false);
  let importError = $state<string | null>(null);
  let importDiagnostics = $state<string | null>(null);
  let validationResult = $state<ImportValidationResult | null>(null);
  let validating = $state(false);
  let importStep = $state<"upload" | "preview" | "importing">("upload");
  let fileInput: HTMLInputElement | undefined = $state();

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
      kitDeploymentId,
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
      importError = err instanceof Error ? err.message : tr("ui.stackImportExportModal.couldNotReadFile");
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
      importError = err instanceof Error ? err.message : tr("ui.stackImportExportModal.couldNotReadFile");
    }
  }

  // Validate the content before import
  async function validateContent() {
    if (!importContent.trim()) {
      importError = tr("ui.stackImportExportModal.noContentFound");
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
      importError = err instanceof Error ? err.message : tr("ui.stackImportExportModal.validationFailed");
      importDiagnostics = buildDiagnostics("validate-import", err);
    } finally {
      validating = false;
    }
  }

  // Perform the import
  async function handleImport() {
    if (!importContent.trim()) {
      importError = tr("ui.stackImportExportModal.noContentToImport");
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
        onSuccess({
          kitDeploymentId: result.kit_deployment_id,
          jobId: result.job_id,
        });
      }

      // Redirect to creating page (same as wizard)
      if (result.redirect) {
        goto(result.redirect);
      } else {
        goto(
          `/stacks/creating?job_id=${result.job_id}&stack_id=${result.kit_deployment_id}`,
        );
      }
    } catch (err) {
      importError = err instanceof Error ? err.message : tr("ui.importExportModal.importFailed");
      importDiagnostics = buildDiagnostics("import", err);
      importStep = "preview";
    } finally {
      importing = false;
    }
  }

  // Handle export
  async function handleExport() {
    if (!kitDeploymentId) {
      exportError = tr("ui.stackImportExportModal.noStackkitDeploymentIsAvailable");
      return;
    }

    exporting = true;
    exportError = null;
    exportDiagnostics = null;

    try {
      const result = await exportKombinationSpec(
        kitDeploymentId,
        exportFormat,
      );

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
      exportError = err instanceof Error ? err.message : tr("ui.importExportModal.exportFailed");
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
  title={mode === "import"
    ? tr("ui.stackImportExportModal.importStackkitDeploymentSpec")
    : tr("ui.stackImportExportModal.exportStackkitDeploymentSpec")}
  {onClose}
  maxWidth="lg"
>
  {#if mode === "import"}
    <!-- IMPORT MODE -->
    {#if importStep === "upload"}
      <div class="space-y-4">
        <p class="text-muted-foreground text-sm">
          {tr("ui.stackImport.intro", { file: "stack-spec.yaml", legacy: "kombination.yaml" })}
        </p>

        <!-- Drag & Drop Zone -->
        <div
          class="border-2 border-dashed border-border rounded-lg p-8 text-center hover:border-primary/50 transition-colors cursor-pointer"
          ondragover={handleDragOver}
          ondrop={handleDrop}
          role="button"
          tabindex="0"
        >
          <svg
            class="w-12 h-12 mx-auto text-muted-foreground mb-4"
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
          <p class="text-muted-foreground mb-2">{tr("ui.stackImportExportModal.dragFileHereOr")}</p>
          <Button variant="secondary" onclick={() => fileInput?.click()}>
            {tr("ui.stackImportExportModal.selectFile")}
          </Button>
          <input
            bind:this={fileInput}
            type="file"
            accept=".yaml,.yml,.json"
            class="hidden"
            onchange={handleFileSelect}
          />
          <p class="text-xs text-muted-foreground mt-2">
            {tr("ui.stackImportExportModal.supportedYamlYmlJson")}
          </p>
        </div>

        <!-- Or paste content -->
        <div class="relative">
          <div class="absolute inset-0 flex items-center">
            <div class="w-full border-t border-border"></div>
          </div>
          <div class="relative flex justify-center text-sm">
            <span class="px-2 bg-card text-muted-foreground">{tr("ui.stackImport.orPaste")}</span>
          </div>
        </div>

        <textarea
          bind:value={importContent}
          placeholder={tr("ui.stackImportExportModal.pasteYourStackSpecYaml")}
          class="w-full h-48 px-4 py-3 bg-input border border-border rounded-lg text-foreground font-mono text-sm placeholder-muted-foreground focus:border-primary focus:outline-none resize-none"
          oninput={() => {
            validationResult = null;
            importError = null;
          }}
        ></textarea>

        {#if importContent && !validationResult}
          <Button
            onclick={validateContent}
            disabled={validating}
            variant="primary"
            class="w-full"
          >
            {#if validating}
              <span class="animate-spin mr-2">⟳</span> {tr("ui.stackImport.validating")}
            {:else}
              {tr("ui.stackImportExportModal.validateConfiguration")}
            {/if}
          </Button>
        {/if}
      </div>
    {:else if importStep === "preview"}
      <!-- Preview Step -->
      <div class="space-y-4">
        {#if validationResult}
          <!-- Validation Status -->
          <div
            class="p-4 rounded-lg {validationResult.valid
              ? 'bg-success/10 border border-success/30'
              : 'bg-destructive/10 border border-destructive/30'}"
          >
            <div class="flex items-center gap-2 mb-2">
              {#if validationResult.valid}
                <svg
                  class="w-5 h-5 text-success"
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
                <span class="text-success font-medium"
                  >{tr("ui.stackImportExportModal.configurationIsValid")}</span
                >
              {:else}
                <svg
                  class="w-5 h-5 text-destructive"
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
                <span class="text-destructive font-medium"
                  >{tr("ui.stackImportExportModal.configurationErrors")}</span
                >
              {/if}
            </div>

            {#if !validationResult.valid && validationResult.errors?.length}
              <ul class="text-sm text-destructive space-y-1 mt-2">
                {#each validationResult.errors as error}
                  <li>
                    <code class="text-destructive">{error.path || "root"}</code>: {error.message}
                  </li>
                {/each}
              </ul>
            {/if}

            {#if validationResult.warnings?.length}
              <ul class="text-sm text-warning space-y-1 mt-2">
                {#each validationResult.warnings as warning}
                  <li>{warning.message}</li>
                {/each}
              </ul>
            {/if}
          </div>

          <!-- Detected Info -->
          {#if validationResult.valid}
            <div data-kx="plate" class="p-4">
              <p class="text-muted-foreground text-sm">
                {tr("ui.stackImportExportModal.allRequiredFieldsArePresent")}
              </p>
            </div>
          {/if}
        {/if}

        <!-- Action Buttons -->
        <div class="flex gap-3">
          <Button onclick={resetImport} variant="secondary" class="flex-1">
            {tr("ui.importExportModal.back")}
          </Button>
          {#if validationResult?.valid}
            <Button
              onclick={handleImport}
              disabled={importing}
              variant="primary"
              class="flex-1"
            >
              {#if importing}
                <span
                  class="mr-2 h-4 w-4 animate-spin rounded-full border-2 border-current border-t-transparent"
                  aria-hidden="true"
                ></span> {tr("ui.importExportModal.importing")}
              {:else}
                {tr("ui.stackImportExportModal.importStartSetup")}
              {/if}
            </Button>
          {/if}
        </div>
      </div>
    {:else if importStep === "importing"}
      <!-- Importing State -->
      <div class="text-center py-8">
        <div
          class="mx-auto mb-4 h-8 w-8 animate-spin rounded-full border-2 border-primary border-t-transparent"
        ></div>
        <p class="text-foreground">{tr("ui.stackImportExportModal.importingConfiguration")}</p>
        <p class="text-muted-foreground text-sm mt-2">
          {tr("ui.stackImportExportModal.youWillBeRedirectedTo")}
        </p>
      </div>
    {/if}

    <!-- Error Display -->
    {#if importError}
      <div
        class="mt-4 p-3 rounded-lg bg-destructive/10 border border-destructive/30 text-destructive text-sm"
      >
        <p class="font-medium">{importError}</p>
        <p class="mt-2 text-xs text-destructive/80">
          {tr("ui.stackImportExportModal.tipIfYouSeeAn")}
        </p>

        {#if importDiagnostics}
          <div class="mt-3 flex gap-2">
            <Button
              variant="secondary"
              onclick={async () => {
                if (importDiagnostics) await copyText(importDiagnostics);
              }}
            >
              {tr("ui.stackImportExportModal.copyDiagnosticsForDevelopers")}
            </Button>
          </div>
        {/if}
      </div>
    {/if}
  {:else}
    <!-- EXPORT MODE -->
    <div class="space-y-4">
      <p class="text-muted-foreground text-sm">
        {tr("ui.stackImport.exportIntro", { file: "stack-spec.yaml" })}
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
          <span class="text-foreground">YAML</span>
          <span class="text-xs text-muted-foreground">{tr("ui.stackImportExportModal.recommended")}</span>
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
          <span class="text-foreground">JSON</span>
        </label>
      </div>

      <!-- Info Box -->
      <div data-kx="plate" class="p-4 text-sm">
        <p class="text-muted-foreground mb-2">
          <strong class="text-foreground">{tr("ui.stackImportExportModal.whatWillBeExported")}</strong>
        </p>
        <ul class="text-muted-foreground space-y-1">
          <li>{tr("ui.stackImportExportModal.stackNameAndConfiguration")}</li>
          <li>{tr("ui.stackImportExportModal.nodeDefinitionsWithoutCredentials")}</li>
          <li>{tr("ui.stackImportExportModal.serviceConfigurations")}</li>
          <li>{tr("ui.stackImportExportModal.systemNetworkAndSecuritySettings")}</li>
          <li>{tr("ui.stackImportExportModal.metadataAndIntents")}</li>
        </ul>
      </div>

      <div
        class="p-3 rounded-lg bg-warning/10 border border-warning/30 text-sm text-warning"
      >
        <strong>{tr("ui.importExportModal.note")}</strong> {tr("ui.stackImportExportModal.sshKeysAndSecretsAre")}
      </div>

      {#if exportError}
        <div
          class="p-3 rounded-lg bg-destructive/10 border border-destructive/30 text-destructive text-sm"
        >
          <p class="font-medium">{exportError}</p>
          <p class="mt-2 text-xs text-destructive/80">
            {tr("ui.stackImportExportModal.ifTheProblemPersistsCopy")}
          </p>

          {#if exportDiagnostics}
            <div class="mt-3 flex gap-2">
              <Button
                variant="secondary"
                onclick={async () => {
                  if (exportDiagnostics) await copyText(exportDiagnostics);
                }}
              >
                {tr("ui.stackImportExportModal.copyDiagnostics")}
              </Button>
            </div>
          {/if}
        </div>
      {/if}

      <div class="flex gap-3">
        <Button onclick={onClose} variant="secondary" class="flex-1">
          {tr("ui.importExportModal.cancel")}
        </Button>
        <Button
          onclick={handleExport}
          disabled={exporting}
          variant="primary"
          class="flex-1"
        >
          {#if exporting}
            <span
              class="mr-2 h-4 w-4 animate-spin rounded-full border-2 border-current border-t-transparent"
              aria-hidden="true"
            ></span> {tr("ui.importExportModal.exporting")}
          {:else}
            {tr("ui.importExportModal.download")}
          {/if}
        </Button>
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
