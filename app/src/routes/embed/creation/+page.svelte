<script lang="ts">
  // The creation flow as a module for a host page (kombify Cloud's use-case
  // popup). The host frames it and hears about it through the creation event
  // contract; wizard, recommendation and lifecycle stay here. Without a process
  // the module only waits, so a host can warm it before a person opens it.
  import { page } from "$app/state";
  import { tr } from "#lib/i18n.svelte.js";
  import AddUseCasesFlow from "#lib/components/hub/AddUseCasesFlow.svelte";
  import ConfigurationFlow from "#lib/components/hub/ConfigurationFlow.svelte";
  import ManagedCreationFlow from "#lib/components/hub/ManagedCreationFlow.svelte";
  import UseCasePlacement from "#lib/components/hub/UseCasePlacement.svelte";
  import LoadingNote from "#lib/components/ui/LoadingNote.svelte";
  import {
    creationFullPath,
    parseCreationEntry,
  } from "#lib/creation/core/index.js";
  import {
    emitCreationEvent,
    markCreationHosted,
    provideCreationEntry,
  } from "#lib/creation/host.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import { creationUseCaseWizardConfig } from "#lib/wizard/self-disclosure-handoff.js";

  const params = $derived(page.url.searchParams);
  const entry = $derived(parseCreationEntry(params));
  provideCreationEntry(() => entry);
  markCreationHosted();
  const initialConfig = $derived(creationUseCaseWizardConfig(params));
  // Steps and targets live in the address; only a new entry starts over.
  const entryKey = $derived(
    entry
      ? [
          entry.process,
          entry.frame,
          entry.frame === "step" ? entry.step : "",
          entry.useCase,
          entry.setup,
          entry.stack,
          entry.server,
        ].join("|")
      : "",
  );

  $effect(() => {
    void entryKey;
    if (authStore.isAuthenticated) emitCreationEvent({ event: "ready" });
  });

  // A session that lapses for a moment and is renewed must not drop the answers.
  let signedIn = $state(false);
  $effect(() => {
    if (authStore.isAuthenticated) signedIn = true;
  });

  let root = $state<HTMLDivElement>();
  $effect(() => {
    if (!root) return;
    let last = 0;
    const observer = new ResizeObserver(() => {
      const height = Math.ceil(root?.scrollHeight ?? 0);
      if (height === last) return;
      last = height;
      emitCreationEvent({ event: "resize", height });
    });
    observer.observe(root);
    return () => observer.disconnect();
  });

  function openFull() {
    if (entry)
      emitCreationEvent({ event: "open-full", path: creationFullPath(entry) });
  }
</script>

<svelte:head>
  <title>{tr("layout.addUseCases.pageTitle")}</title>
</svelte:head>

<div
  bind:this={root}
  class="min-w-0"
  data-testid="creation-embed"
  data-process={entry?.process ?? "idle"}
>
  {#if entry}
    {#if !signedIn}
      <LoadingNote label={tr("common.loading")} />
    {:else}
      {#key entryKey}
        {#if entry.process === "add-use-case" && entry.stack}
          <AddUseCasesFlow stackId={entry.stack} {initialConfig} embedded />
        {:else if entry.process === "add-use-case"}
          <UseCasePlacement
            {initialConfig}
            preferredServerId={entry.server ?? ""}
          />
        {:else if entry.process === "add-server" && entry.stack}
          <ManagedCreationFlow stackId={entry.stack} embedded />
        {:else if entry.process === "place-on-server" && entry.server}
          <ConfigurationFlow
            {initialConfig}
            existingServerId={entry.server}
            embedded
          />
        {:else if entry.process === "stack"}
          <ConfigurationFlow {initialConfig} embedded />
        {/if}
      {/key}
      <div class="px-6 pb-6">
        <button
          type="button"
          class="text-sm text-muted-foreground underline underline-offset-2 hover:text-foreground"
          data-testid="creation-embed-open-full"
          onclick={openFull}
        >
          {tr("creation.embed.continueFull")}
        </button>
      </div>
    {/if}
  {/if}
</div>
