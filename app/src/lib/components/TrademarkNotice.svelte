<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";

  let { class: className = "" }: { class?: string } = $props();
</script>

<!-- Shown under a list of applications, whose logos identify the software the
     owner runs. The Credits page lists what the recognition builds on. -->
<p
  class="text-xs leading-relaxed text-muted-foreground {className}"
  data-testid="trademark-notice"
>
  {tr("ui.appLogo.notice")}
  <a
    href="/credits"
    class="underline underline-offset-2 hover:text-foreground"
    data-testid="trademark-credits-link">{tr("ui.appLogo.credits")}</a
  >
</p>
