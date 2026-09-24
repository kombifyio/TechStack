<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import type { SmartHomeRecommendation } from "#lib/api/unifier.js";
  let { recommendation }: { recommendation?: SmartHomeRecommendation } =
    $props();
</script>

{#if recommendation}
  <aside
    class="rounded-lg border border-border bg-muted/30 p-4 text-sm space-y-2"
    aria-label={tr("wizard.smartHome.advice")}
  >
    <p class="font-medium">{tr("wizard.smartHome.advice")}</p>
    <p>
      {recommendation.management_scope === "observed"
        ? tr("wizard.smartHome.preserve")
        : recommendation.operating_form === "haos"
          ? tr("wizard.smartHome.haos")
          : tr("wizard.smartHome.container")}
    </p>
    {#if recommendation.requirements.length}
      <ul class="list-disc pl-5 space-y-1">
        {#each recommendation.requirements as requirement}
          <li>{tr(`wizard.smartHome.${requirement}`)}</li>
        {/each}
      </ul>
    {/if}
  </aside>
{/if}
