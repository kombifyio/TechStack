<script lang="ts">
  import ConfigurationFlow from "#lib/components/hub/ConfigurationFlow.svelte";
  import { tr } from "#lib/i18n.svelte.js";
</script>

<svelte:head>
  <title>{tr("wizard.title")} | kombify-Techstack</title>
</svelte:head>

<ConfigurationFlow />
