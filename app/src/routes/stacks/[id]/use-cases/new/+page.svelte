<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { page } from "$app/state";
  import { creationUseCaseWizardConfig } from "#lib/wizard/self-disclosure-handoff.js";
  import AddUseCasesFlow from "#lib/components/hub/AddUseCasesFlow.svelte";

  const stackId = $derived(page.params.id ?? "");
  const initialConfig = $derived(
    creationUseCaseWizardConfig(page.url.searchParams),
  );
  const initialGoals = $derived(
    (page.url.searchParams.get("goals") ?? "")
      .split(",")
      .map((goal) => goal.trim())
      .filter(Boolean),
  );
</script>

<svelte:head>
  <title>{tr("layout.addUseCases.pageTitle")}</title>
</svelte:head>

{#key `${stackId}:${page.url.search}`}
  <AddUseCasesFlow {stackId} {initialGoals} {initialConfig} />
{/key}
