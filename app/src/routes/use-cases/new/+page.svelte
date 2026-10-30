<script lang="ts">
  // Adding use cases anywhere in the homelab: the owner picks one of the
  // existing StackKits or enrolled servers (or a new server), then the use
  // cases on that target's Goals step. `?server=` preselects a server or the
  // StackKit it runs; `?use-case=` and `?setup=` carry a chosen use case.
  import { page } from "$app/state";
  import { tr } from "#lib/i18n.svelte.js";
  import UseCasePlacement from "#lib/components/hub/UseCasePlacement.svelte";
  import { creationUseCaseWizardConfig } from "#lib/wizard/self-disclosure-handoff.js";

  const initialConfig = $derived(
    creationUseCaseWizardConfig(page.url.searchParams),
  );
  const preferredServerId = $derived(page.url.searchParams.get("server") ?? "");
</script>

<svelte:head>
  <title>{tr("layout.addUseCases.pageTitle")}</title>
</svelte:head>

{#key `${page.url.searchParams.get("use-case") ?? ""}:${page.url.searchParams.get("setup") ?? ""}:${preferredServerId}`}
  <UseCasePlacement {initialConfig} {preferredServerId} />
{/key}
