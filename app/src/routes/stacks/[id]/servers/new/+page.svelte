<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { page } from "$app/state";
  import ManagedCreationFlow from "#lib/components/hub/ManagedCreationFlow.svelte";

  const stackId = $derived(page.params.id ?? "");
</script>

<svelte:head>
  <title>{tr("ui.stacksIdServersNew.addNodeKombifyTechstack")}</title>
</svelte:head>

<ManagedCreationFlow {stackId} />
