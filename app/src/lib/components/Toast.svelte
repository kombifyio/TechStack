<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  /**
   * Toast - short confirmations and non-blocking notices.
   *
   * The stack is a manual popover, so it sits in the browser's top layer and
   * stays visible above an open modal dialog (a native `<dialog>` otherwise
   * covers everything with a z-index). Each toast is a `menu-plate` surface
   * with the state hue carried by the icon and an accent edge, never by text
   * on a translucent tint: contrast comes from the card and foreground
   * tokens, so it holds in light and dark.
   */
  import { toasts, type Toast } from "#lib/stores/toast.js";
  import { fly, fade } from "svelte/transition";
  import { CheckCircle, XCircle, AlertTriangle, Info, X } from "@lucide/svelte";

  const iconMap = {
    success: CheckCircle,
    error: XCircle,
    warning: AlertTriangle,
    info: Info,
  };

  const edgeMap: Record<Toast["type"], string> = {
    success: "bg-success",
    error: "bg-destructive",
    warning: "bg-warning",
    info: "bg-info",
  };
  const iconMapColor: Record<Toast["type"], string> = {
    success: "text-success",
    error: "text-destructive",
    warning: "text-warning",
    info: "text-info",
  };

  let host = $state<HTMLDivElement | null>(null);
  let lastShownId = "";

  // Keep the stack in the top layer while it has toasts. A toast that arrives
  // after a dialog opened is raised above it by showing the popover again.
  $effect(() => {
    const element = host;
    if (!element || typeof element.showPopover !== "function") return;
    const newest = $toasts.at(-1)?.id ?? "";
    if (!newest) {
      lastShownId = "";
      if (element.matches(":popover-open")) element.hidePopover();
      return;
    }
    if (newest === lastShownId) return;
    lastShownId = newest;
    if (element.matches(":popover-open")) element.hidePopover();
    element.showPopover();
  });
</script>

<div
  bind:this={host}
  popover="manual"
  class="toast-stack flex flex-col gap-2 pointer-events-none"
>
  {#each $toasts as toast (toast.id)}
    {@const Icon = iconMap[toast.type]}
    <div
      data-kx="menu-plate"
      role={toast.type === "error" || toast.type === "warning"
        ? "alert"
        : "status"}
      class="pointer-events-auto relative flex items-start gap-3 overflow-hidden rounded-xl border border-border py-4 ps-5 pe-4 shadow-2xl text-foreground"
      in:fly={{ x: 100, duration: 250, delay: 50 }}
      out:fade={{ duration: 150 }}
    >
      <!-- The state hue is an accent edge and the icon; the text stays on the
           card with the foreground token. -->
      <span
        class="absolute inset-y-0 start-0 w-1 {edgeMap[toast.type]}"
        aria-hidden="true"
      ></span>
      <Icon
        class="w-5 h-5 shrink-0 mt-0.5 {iconMapColor[toast.type]}"
        aria-hidden="true"
      />
      <div class="flex-1 min-w-0 text-foreground">
        <p class="font-medium leading-tight">{toast.message}</p>
        {#if toast.details}
          <p class="text-sm text-muted-foreground mt-1 break-words">
            {toast.details}
          </p>
        {/if}
      </div>
      <button
        type="button"
        class="text-muted-foreground hover:text-foreground transition-colors rounded-md p-0.5 hover:bg-muted -mr-1 -mt-1"
        onclick={() => toasts.remove(toast.id)}
        aria-label={tr("ui.toast.dismiss")}
      >
        <X class="w-4 h-4" />
      </button>
    </div>
  {/each}
</div>

<style>
  /* The popover UA style centres the element; this stack hangs from the
     top corner like the fixed container it replaces. */
  .toast-stack {
    position: fixed;
    inset: 1rem 1rem auto auto;
    margin: 0;
    padding: 0;
    border: 0;
    background: transparent;
    overflow: visible;
    max-width: min(28rem, calc(100vw - 2rem));
    width: max-content;
  }
  .toast-stack:not(:popover-open) {
    display: none;
  }
</style>
