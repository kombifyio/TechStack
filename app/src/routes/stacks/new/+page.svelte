<script lang="ts">
  import { page } from "$app/state";
  import { replaceState } from "$app/navigation";
  import ConfigurationFlow from "#lib/components/hub/ConfigurationFlow.svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import {
    readSelfDisclosureHandoff,
    withoutSelfDisclosureParams,
  } from "#lib/wizard/self-disclosure-handoff.js";

  // Answers handed over from the kombify Cloud capture surface. Read once:
  // after they are stored, the URL drops them so a reload does not resend.
  const selfDisclosure = readSelfDisclosureHandoff(page.url.searchParams);

  function clearHandoffParams() {
    replaceState(withoutSelfDisclosureParams(page.url), page.state);
  }
</script>

<svelte:head>
  <title>{tr("wizard.title")} | kombify-Techstack</title>
</svelte:head>

<ConfigurationFlow
  {selfDisclosure}
  onSelfDisclosureSettled={clearHandoffParams}
/>
