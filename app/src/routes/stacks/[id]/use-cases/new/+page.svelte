<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { page } from "$app/state";
  import AddUseCasesFlow from "#lib/components/hub/AddUseCasesFlow.svelte";

  const stackId = $derived(page.params.id ?? "");
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

<AddUseCasesFlow {stackId} {initialGoals} />
