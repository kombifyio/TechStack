<script lang="ts">
  import { page } from "$app/state";
  import CreationWizardPreview from "#lib/components/wizard/CreationWizardPreview.svelte";
  import { tr } from "#lib/i18n.svelte.js";
</script>

<svelte:head>
  <title>{tr("wizard.preview.label")} | kombify Techstack</title>
  <meta name="robots" content="noindex, nofollow" />
</svelte:head>

<CreationWizardPreview
  initialVariant={page.url.searchParams.get("variant") === "b"
    ? "focus"
    : "discover"}
/>
