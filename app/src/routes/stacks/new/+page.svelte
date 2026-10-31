<script lang="ts">
  import { untrack } from "svelte";
  import { page } from "$app/state";
  import { replaceState } from "$app/navigation";
  import UseCasePlacement from "#lib/components/hub/UseCasePlacement.svelte";
  import ConfigurationFlow from "#lib/components/hub/ConfigurationFlow.svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import {
    creationUseCaseHandoffKey,
    creationUseCaseWizardConfig,
    readSelfDisclosureHandoff,
    withoutSelfDisclosureParams,
  } from "#lib/wizard/self-disclosure-handoff.js";

  // Answers handed over from the kombify Cloud capture surface. Read once:
  // after they are stored, the URL drops them so a reload does not resend.
  let selfDisclosure = $state(readSelfDisclosureHandoff(page.url.searchParams));
  // A link with answers keeps the creation flow, also once they are stored.
  const handedOver = untrack(() => Boolean(selfDisclosure));
  const creationUseCase = $derived(
    creationUseCaseHandoffKey(page.url.searchParams),
  );
  const initialConfig = $derived(
    creationUseCaseWizardConfig(page.url.searchParams),
  );

  function clearHandoffParams() {
    selfDisclosure = null;
    // The wizard keeps its target and step in shallow history entries.
    replaceState(
      withoutSelfDisclosureParams(page.shallow?.url ?? page.url),
      page.state,
    );
  }
</script>

<svelte:head>
  <title>{tr("wizard.title")} | kombify-Techstack</title>
</svelte:head>

{#key `${creationUseCase}:${page.url.searchParams.get("setup") ?? ""}`}
  {#if creationUseCase && initialConfig}
    <UseCasePlacement
      {initialConfig}
      {selfDisclosure}
      onSelfDisclosureSettled={clearHandoffParams}
    />
  {:else if handedOver}
    <ConfigurationFlow
      {initialConfig}
      {selfDisclosure}
      onSelfDisclosureSettled={clearHandoffParams}
    />
  {:else}
    <!-- The owner's existing servers are offered on the Server step. -->
    <UseCasePlacement />
  {/if}
{/key}
