<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { Check, ChevronDown, Copy } from "@lucide/svelte";
  import { creation } from "./creation-controller.svelte.js";

  // The command for the person's own server. It is the body of the "next
  // step" card, so it brings no card or heading of its own.
  const commandCopied = $derived(creation.copiedCommand === "main");
  const copyAnnouncement = $derived(
    creation.copiedCommand === "main"
      ? tr("ui.common.copiedCheck")
      : creation.copiedCommand === "error:main"
        ? tr("ui.stacksCreatingCreationRunStatus.copyFailedSelectTheCommand")
        : "",
  );
</script>

{#if creation.installCommandVisible}
  <div data-testid="install-command-block">
    {#if creation.oneLinerPreviewRequired}
      <p class="text-sm text-muted-foreground mb-4">
        {tr("ui.stacksCreatingCreationInstallCommand.runThisCommandOnThe")}
      </p>
      {#if creation.simulationPreviewUrl}
        <a
          href={creation.simulationPreviewUrl}
          target="_blank"
          rel="noopener noreferrer"
          class="inline-flex items-center text-sm text-primary hover:underline mb-4"
          data-testid="simulation-preview-link"
        >
          {tr("ui.stacksCreatingCreationInstallCommand.openOneHourSimulateDemo")}
        </a>
      {/if}
      {#if creation.simulationPreviewExpiresAt}
        <p class="text-xs text-muted-foreground mb-4">
          {tr("ui.creationInstall.previewExpires", {
            time: creation.simulationPreviewExpiresAt,
          })}
        </p>
      {:else if creation.simulationPreviewUrl}
        <p class="text-xs text-muted-foreground mb-4">
          {tr("ui.stacksCreatingCreationInstallCommand.previewLifetimeIsLimitedTo")}
        </p>
      {/if}
    {:else}
      <p class="text-sm text-muted-foreground mb-4">
        {tr("ui.stacksCreatingCreationInstallCommand.runThisCommandOnAll")}
      </p>
    {/if}

    <!-- Server URL Override -->
    <div class="mb-4">
      <label for="server-url" class="block text-xs text-muted-foreground mb-1">
        {tr("ui.stacksCreatingCreationInstallCommand.kombifyTechstackServerUrlReachable")}
      </label>
      <input
        id="server-url"
        type="text"
        bind:value={creation.serverUrl}
        class="w-full bg-card border border-border rounded px-3 py-2 text-sm text-foreground focus:outline-none focus:border-primary transition-colors"
        placeholder="http://192.168.x.x:5261"
      />
    </div>

    <!-- Main install command -->
    <div class="relative mb-2">
      <!-- A scrollable code block must be reachable by keyboard. -->
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <pre
        class="px-4 py-3 bg-card rounded-lg text-primary text-sm font-mono overflow-x-auto pr-12"
        tabindex="0"
        aria-label={tr("creation.install.command")}>{creation.installCommand}</pre>
      <button
        type="button"
        onclick={() => creation.copyToClipboard(creation.installCommand, "main")}
        data-kx="control"
        class="absolute right-2 top-1/2 -translate-y-1/2 rounded px-2 py-1"
        title={tr("ui.stacksCreatingCreationInstallCommand.copy")}
        aria-label={tr(
          "ui.stacksCreatingCreationInstallCommand.copyInstallationCommandToClipboard",
        )}
        data-testid="copy-install-command"
      >
        {#if commandCopied}
          <Check class="w-4 h-4 text-success" aria-hidden="true" />
        {:else}
          <Copy class="w-4 h-4" aria-hidden="true" />
        {/if}
      </button>
    </div>
    <p class="mb-4 min-h-4 text-xs text-muted-foreground" role="status">
      {copyAnnouncement}
    </p>

    <!-- Alternative commands -->
    <button
      type="button"
      onclick={() => creation.toggleAllCommands()}
      aria-expanded={creation.showAllCommands}
      aria-controls="install-command-alternatives"
      class="text-sm text-muted-foreground hover:text-foreground/80 flex items-center gap-1"
    >
      <ChevronDown
        class="w-4 h-4 transition-transform {creation.showAllCommands
          ? 'rotate-180'
          : ''}"
        aria-hidden="true"
      />
      {tr("ui.stacksCreatingCreationInstallCommand.alternativeInstallationMethods")}
    </button>

    {#if creation.showAllCommands}
      <div id="install-command-alternatives" class="mt-4 space-y-3">
        {#each creation.allInstallCommands as cmd (cmd.platform)}
          <div data-kx="plate" class="p-3">
            <p class="text-xs text-muted-foreground mb-2">
              {cmd.description}
            </p>
            <div class="relative">
              <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
              <pre
                class="text-xs font-mono text-foreground/80 overflow-x-auto pr-10"
                tabindex="0">{cmd.command}</pre>
              <button
                type="button"
                onclick={() =>
                  creation.copyToClipboard(cmd.command, cmd.platform)}
                class="absolute right-1 top-1/2 -translate-y-1/2 p-1 hover:bg-secondary rounded"
                title={tr("ui.stacksCreatingCreationInstallCommand.copy")}
                aria-label={tr(
                  "creation.install.copyFor",
                  { description: cmd.description },
                )}
              >
                {#if creation.copiedCommand === cmd.platform}
                  <Check class="w-3 h-3 text-success" aria-hidden="true" />
                {:else}
                  <Copy class="w-3 h-3 text-muted-foreground" aria-hidden="true" />
                {/if}
              </button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </div>
{/if}
