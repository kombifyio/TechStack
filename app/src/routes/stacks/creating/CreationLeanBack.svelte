<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
</script>

<!-- Greets the user while the run works: their part is done. -->
<div
  class="flex max-w-md items-start gap-3 rounded-2xl bg-primary/5 px-5 py-4"
  data-testid="creation-lean-back"
  role="status"
>
  <svg
    class="mt-0.5 h-5 w-5 shrink-0 text-primary"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.75"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9z" />
    <path d="M19 15l.8 2.2L22 18l-2.2.8L19 21l-.8-2.2L16 18l2.2-.8z" />
  </svg>
  <div>
    <p class="text-base font-semibold text-foreground">
      {tr("ui.stacksCreating.leanBackTitle")}
    </p>
    <p class="mt-1 text-sm text-muted-foreground">
      {tr("ui.stacksCreating.leanBackBody")}
    </p>
  </div>
</div>
