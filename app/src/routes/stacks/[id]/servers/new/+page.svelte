<script lang="ts">
  import { page } from "$app/state";
  import ManagedCreationFlow from "#lib/components/hub/ManagedCreationFlow.svelte";

  const stackId = $derived(page.params.id ?? "");
</script>

<svelte:head>
  <title>Add Node | kombify-Techstack</title>
</svelte:head>

<ManagedCreationFlow {stackId} />
