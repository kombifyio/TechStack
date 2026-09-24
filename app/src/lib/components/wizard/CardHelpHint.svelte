<!--
  Opt-in help on wizard selection cards.

  Hover must not own this: the plate used to open on ? hover, cover the
  card, and steal clicks so the tile could not be selected. Click the ?
  to open; the plate ignores pointer events so the card stays the target.
-->
<script lang="ts">
  import { TRANSIENT_PANEL_DISMISS_EVENT } from "@kombiverselabs/ui/shell";
  import { tr } from "#lib/i18n.svelte.js";

  interface Props {
    helpText: string;
    helpTip?: string;
  }

  let { helpText, helpTip }: Props = $props();

  let open = $state(false);
  let root = $state<HTMLSpanElement | null>(null);

  function toggle(event: MouseEvent) {
    event.preventDefault();
    event.stopPropagation();
    open = !open;
  }

  function keepCardIdle(event: Event) {
    event.stopPropagation();
  }

  // Dismiss on the bubbling click, not capturing pointerdown. Closing the
  // plate during pointerdown would swallow the click that should select the
  // tile underneath.
  $effect(() => {
    if (!open) return;
    const onClick = (event: MouseEvent) => {
      const target = event.target;
      if (target instanceof Node && root?.contains(target)) return;
      open = false;
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") open = false;
    };
    const onNavigate = () => {
      open = false;
    };
    document.addEventListener("click", onClick);
    document.addEventListener("keydown", onKeyDown);
    window.addEventListener(TRANSIENT_PANEL_DISMISS_EVENT, onNavigate);
    return () => {
      document.removeEventListener("click", onClick);
      document.removeEventListener("keydown", onKeyDown);
      window.removeEventListener(TRANSIENT_PANEL_DISMISS_EVENT, onNavigate);
    };
  });
</script>

<span bind:this={root} class="relative shrink-0 self-start">
  <button
    type="button"
    aria-label={tr("common.moreInfo")}
    aria-expanded={open}
    onclick={toggle}
    onpointerdown={keepCardIdle}
    class="rounded-full bg-muted px-2 py-1 text-xs text-muted-foreground transition hover:bg-muted/80"
  >
    ?
  </button>
  {#if open}
    <div
      data-kx="plate"
      role="tooltip"
      class="!pointer-events-none absolute left-0 top-full z-30 mt-2 w-[min(18rem,calc(100vw-3rem))] rounded-lg p-4 shadow-xl"
    >
      <p class="text-sm text-foreground">{helpText}</p>
      {#if helpTip}
        <p class="mt-3 text-xs italic text-muted-foreground">
          {tr("common.tip")}: {helpTip}
        </p>
      {/if}
    </div>
  {/if}
</span>
