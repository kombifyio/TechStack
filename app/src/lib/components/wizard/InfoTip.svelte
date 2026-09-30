<!--
  Small info icon that explains one wizard option and links its guide.

  It opens on hover (pointer devices), on keyboard focus and on click (touch),
  and stays open while the pointer is over the icon or the plate so the guide
  link can be reached. Unlike CardHelpHint the plate takes pointer events, so
  the tip is always a sibling of the option it explains, never inside its
  <button> or <label>: nested, it would join the option's accessible name and
  a click on the plate would select the option.

  placement="corner" pins the tip to the top-end corner of the nearest
  positioned ancestor; the host sets --info-tip-top / --info-tip-end.
-->
<script lang="ts">
  import { tick } from "svelte";
  import { Info, ArrowUpRight } from "@lucide/svelte";
  import { TRANSIENT_PANEL_DISMISS_EVENT } from "@kombiverselabs/ui/shell";
  import { tr } from "#lib/i18n.svelte.js";
  import { docsUrl } from "#lib/docs-links.js";

  interface Props {
    /** Name of the explained option, for the button's accessible label. */
    topic: string;
    text: string;
    /** docs.kombify.io path, optionally with `#anchor`. */
    docs?: string;
    placement?: "inline" | "corner";
  }

  let { topic, text, docs, placement = "inline" }: Props = $props();

  const plateId = `info-tip-${Math.random().toString(36).slice(2, 10)}`;
  const EDGE = 8;
  let open = $state(false);
  let pinned = $state(false);
  // Horizontal nudge that keeps the plate inside the viewport.
  let shift = $state(0);
  let root = $state<HTMLSpanElement | null>(null);
  let plate = $state<HTMLSpanElement | null>(null);
  let closeTimer: ReturnType<typeof setTimeout> | undefined;

  async function show() {
    clearTimeout(closeTimer);
    if (open) return;
    shift = 0;
    open = true;
    await tick();
    const rect = plate?.getBoundingClientRect();
    if (!rect) return;
    const maxRight = document.documentElement.clientWidth - EDGE;
    if (rect.left < EDGE) shift = EDGE - rect.left;
    else if (rect.right > maxRight) shift = maxRight - rect.right;
  }

  function scheduleClose() {
    if (pinned) return;
    clearTimeout(closeTimer);
    // Grace period so the pointer can travel from the icon to the plate.
    closeTimer = setTimeout(() => (open = false), 160);
  }

  function close() {
    clearTimeout(closeTimer);
    open = false;
    pinned = false;
  }

  function toggle() {
    if (pinned) close();
    else {
      pinned = true;
      void show();
    }
  }

  function onPointerEnter(event: PointerEvent) {
    if (event.pointerType === "mouse") void show();
  }

  function onPointerLeave(event: PointerEvent) {
    if (event.pointerType === "mouse") scheduleClose();
  }

  function onFocusOut(event: FocusEvent) {
    const next = event.relatedTarget;
    if (next instanceof Node && root?.contains(next)) return;
    close();
  }

  $effect(() => {
    if (!open) return;
    const onClick = (event: MouseEvent) => {
      const target = event.target;
      if (target instanceof Node && root?.contains(target)) return;
      close();
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") close();
    };
    document.addEventListener("click", onClick);
    document.addEventListener("keydown", onKeyDown);
    window.addEventListener(TRANSIENT_PANEL_DISMISS_EVENT, close);
    return () => {
      document.removeEventListener("click", onClick);
      document.removeEventListener("keydown", onKeyDown);
      window.removeEventListener(TRANSIENT_PANEL_DISMISS_EVENT, close);
    };
  });

  $effect(() => () => clearTimeout(closeTimer));
</script>

<span
  bind:this={root}
  class="info-tip"
  data-placement={placement}
  role="presentation"
  onpointerenter={onPointerEnter}
  onpointerleave={onPointerLeave}
  onfocusout={onFocusOut}
>
  <button
    type="button"
    class="info-trigger"
    aria-label={tr("wizard.tip.moreAbout").replace("{topic}", topic)}
    aria-expanded={open}
    aria-controls={open ? plateId : undefined}
    data-testid="info-tip"
    onclick={toggle}
    onfocus={() => void show()}
  >
    <Info size={14} strokeWidth={1.8} aria-hidden="true" />
  </button>
  {#if open}
    <span
      class="info-plate"
      id={plateId}
      role="note"
      style:translate={`${shift}px 0`}
      bind:this={plate}
    >
      <span class="info-text">{text}</span>
      {#if docs}
        <a
          href={docsUrl(docs)}
          target="_blank"
          rel="noopener noreferrer"
          data-testid="info-tip-docs"
          >{tr("wizard.tip.readGuide")}<ArrowUpRight
            size={13}
            aria-hidden="true"
          /></a
        >
      {/if}
    </span>
  {/if}
</span>

<style>
  .info-tip {
    position: relative;
    display: inline-flex;
    flex: none;
    vertical-align: middle;
  }
  .info-tip[data-placement="corner"] {
    position: absolute;
    top: var(--info-tip-top, 12px);
    inset-inline-end: var(--info-tip-end, 12px);
    z-index: 2;
  }
  .info-trigger {
    display: grid;
    place-items: center;
    width: 24px;
    height: 24px;
    border-radius: 999px;
    color: var(--muted-foreground);
    cursor: help;
    transition: color 150ms;
  }
  .info-trigger:hover,
  .info-trigger[aria-expanded="true"] {
    color: var(--primary);
  }
  .info-trigger:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 1px;
  }
  .info-plate {
    position: absolute;
    top: calc(100% + 4px);
    inset-inline-end: -6px;
    z-index: 40;
    display: grid;
    gap: 10px;
    width: min(18rem, calc(100vw - 2rem));
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--popover, var(--card));
    color: var(--popover-foreground, var(--foreground));
    box-shadow: 0 12px 32px -12px rgb(0 0 0 / 0.45);
    font-size: 12.5px;
    font-weight: 400;
    line-height: 1.6;
    text-align: start;
    text-transform: none;
    letter-spacing: normal;
    white-space: normal;
    animation: tip-in 120ms ease-out;
  }
  .info-plate a {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    width: fit-content;
    color: var(--primary);
    font-size: 12px;
    text-underline-offset: 3px;
  }
  .info-plate a:hover {
    text-decoration: underline;
  }
  .info-plate a:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 2px;
  }
  @keyframes tip-in {
    from {
      opacity: 0;
      transform: translateY(-3px);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .info-plate {
      animation: none;
    }
  }
</style>
