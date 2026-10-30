<script lang="ts">
  import { page } from "$app/state";
  import { tr } from "#lib/i18n.svelte.js";
  import UseCaseRemoveFlow from "#lib/components/hub/UseCaseRemoveFlow.svelte";
  const stackId = $derived(page.params.id ?? "");
</script>

<svelte:head
  ><title>{tr("removeUseCase.title")} | kombify-Techstack</title></svelte:head
>
{#key stackId}<UseCaseRemoveFlow {stackId} />{/key}
