<!--
  OverlayPanel - details that float above the page next to the control that
  opened them. It is fixed to the viewport and rendered outside the list it
  belongs to, so opening it never resizes a card, moves another card or
  scrolls the list, and no scroll container clips it. It closes on Escape, on
  a press outside, and when the host asks transient panels to dismiss. It is a labelled group,
  not a dialog: it does not trap focus or block the page.

  The host owns the opening control (the `anchor`) and the open state.
-->
<script lang="ts">
  import { tick, type Snippet } from "svelte";
  import { TRANSIENT_PANEL_DISMISS_EVENT } from "@kombiverselabs/ui/shell";

  interface Props {
    anchor: HTMLElement | null;
    onClose: () => void;
    /** Accessible name of the panel. */
    label: string;
    testId?: string;
    /** Preferred width in px; never wider than the viewport. */
    width?: number;
    children: Snippet;
  }

  let {
    anchor,
    onClose,
    label,
    testId,
    width = 440,
    children,
  }: Props = $props();

  const EDGE = 8;
  const GAP = 6;
  let panel = $state<HTMLDivElement | null>(null);
  // Unset until measured, so the first paint never shows it misplaced.
  let place = $state<{
    left: number;
    top: number;
    width: number;
    maxHeight: number;
  } | null>(null);

  /** Moves the node to <body> so no ancestor clips or transforms it. */
  function portal(node: HTMLElement) {
    document.body.appendChild(node);
    return { destroy: () => node.remove() };
  }

  function reposition() {
    if (!anchor || !panel) return;
    const viewportWidth = document.documentElement.clientWidth;
    const viewportHeight = window.innerHeight;
    const rect = anchor.getBoundingClientRect();
    const panelWidth = Math.min(width, viewportWidth - EDGE * 2);
    const left = Math.min(
      Math.max(rect.left, EDGE),
      viewportWidth - panelWidth - EDGE,
    );
    const height = panel.scrollHeight;
    const roomBelow = viewportHeight - rect.bottom - GAP - EDGE;
    const roomAbove = rect.top - GAP - EDGE;
    const above = height > roomBelow && roomAbove > roomBelow;
    const maxHeight = Math.max(120, above ? roomAbove : roomBelow);
    const used = Math.min(height, maxHeight);
    place = {
      left,
      top: above ? rect.top - GAP - used : rect.bottom + GAP,
      width: panelWidth,
      maxHeight,
    };
  }

  $effect(() => {
    if (!anchor || !panel) return;
    void tick().then(async () => {
      reposition();
      await tick();
      panel?.focus({ preventScroll: true });
    });
    const observer = new ResizeObserver(reposition);
    observer.observe(panel);
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target;
      if (!(target instanceof Node)) return;
      if (panel?.contains(target) || anchor?.contains(target)) return;
      // A confirm or terminal dialog opened from inside the panel is part of
      // the same interaction; pressing in it must not pull the panel away.
      if (target instanceof Element && target.closest('[role="dialog"], [role="alertdialog"]')) return;
      onClose();
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented) return;
      onClose();
      anchor?.focus({ preventScroll: true });
    };
    document.addEventListener("pointerdown", onPointerDown, true);
    document.addEventListener("keydown", onKeyDown);
    window.addEventListener("resize", reposition);
    window.addEventListener("scroll", reposition, true);
    window.addEventListener(TRANSIENT_PANEL_DISMISS_EVENT, onClose);
    return () => {
      observer.disconnect();
      document.removeEventListener("pointerdown", onPointerDown, true);
      document.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("resize", reposition);
      window.removeEventListener("scroll", reposition, true);
      window.removeEventListener(TRANSIENT_PANEL_DISMISS_EVENT, onClose);
    };
  });
</script>

<div
  use:portal
  bind:this={panel}
  role="group"
  aria-label={label}
  tabindex="-1"
  data-testid={testId}
  class="overlay-panel fixed z-40 overflow-y-auto rounded-xl border border-border bg-card text-card-foreground shadow-xl outline-none"
  style:visibility={place ? "visible" : "hidden"}
  style:left="{place?.left ?? 0}px"
  style:top="{place?.top ?? 0}px"
  style:width="{place?.width ?? width}px"
  style:max-height="{place?.maxHeight ?? 400}px"
>
  {@render children()}
</div>

<style>
  .overlay-panel {
    overscroll-behavior: contain;
  }
</style>
