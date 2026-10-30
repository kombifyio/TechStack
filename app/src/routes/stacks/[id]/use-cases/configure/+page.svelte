<script lang="ts">
  import { page } from "$app/state";
  import { tr } from "#lib/i18n.svelte.js";
  import UseCaseConfigureFlow from "#lib/components/hub/UseCaseConfigureFlow.svelte";
  const stackId = $derived(page.params.id ?? "");
  const preferredWorkload = $derived(
    page.url.searchParams.get("workload") ?? "",
  );
</script>

<svelte:head
  ><title>{tr("configure.title")} | kombify-Techstack</title></svelte:head
>

{#key `${stackId}:${preferredWorkload}`}
  <UseCaseConfigureFlow {stackId} {preferredWorkload} />
{/key}
