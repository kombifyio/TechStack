<script lang="ts">
  import { onMount } from "svelte";
  import { tr } from "#lib/i18n.svelte.js";
  /**
   * Modal - the one dialog primitive of the operator UI.
   *
   * It is a native modal `<dialog>` opened with `showModal()`, so the browser
   * owns what a hand-built overlay always got wrong: everything behind the
   * dialog is inert (focus cannot leave it, Tab cycles inside), Escape closes
   * only the top-most dialog (stacked dialogs close one by one) and the page
   * behind cannot be operated. This component adds the rest of the contract:
   * a unique title id, a sensible initial focus (the first control of the
   * content, then of the footer, never the close button of a confirmation),
   * and focus returned to the element that opened it.
   *
   * Uses CSS variables for theming; the surface keeps the card look.
   */
  import type { Snippet } from "svelte";
  import { X } from "@lucide/svelte";

  interface Props {
    title?: string;
    description?: string;
    onClose: () => void;
    maxWidth?: "sm" | "md" | "lg" | "xl" | "2xl" | "3xl" | "full";
    showClose?: boolean;
    /** `alertdialog` for a question that interrupts: confirm, destroy, remove. */
    role?: "dialog" | "alertdialog";
    /** `danger` colors the title like the action it asks to confirm. */
    tone?: "default" | "danger";
    /** Accessible name when there is no visible title. */
    ariaLabel?: string;
    testId?: string;
    children: Snippet;
    footer?: Snippet;
  }

  let {
    title,
    description,
    onClose,
    maxWidth = "lg",
    showClose = true,
    role = "dialog",
    tone = "default",
    ariaLabel,
    testId,
    children,
    footer,
  }: Props = $props();

  // One id per instance: two open dialogs (a confirmation above a form) must
  // never share a title id.
  const uid = $props.id();
  const titleId = `${uid}-title`;
  const descriptionId = `${uid}-description`;

  let dialog = $state<HTMLDialogElement | null>(null);
  let closing = false;

  const maxWidthClasses: Record<string, string> = {
    sm: "max-w-sm",
    md: "max-w-md",
    lg: "max-w-lg",
    xl: "max-w-xl",
    "2xl": "max-w-2xl",
    "3xl": "max-w-3xl",
    full: "max-w-[95vw]",
  };

  const TABBABLE =
    'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

  /**
   * First control the person most likely wants: an explicit `data-autofocus`
   * or `autofocus`, otherwise the first control of the content, then of the
   * footer. The close button is skipped on purpose; landing on "X" of a
   * confirmation is never the intent.
   */
  function initialFocus(root: HTMLDialogElement) {
    const explicit = root.querySelector<HTMLElement>(
      "[data-autofocus], [autofocus]",
    );
    const target =
      explicit ??
      root.querySelector<HTMLElement>(
        `[data-modal-content] :is(${TABBABLE}), [data-modal-footer] :is(${TABBABLE})`,
      ) ??
      root.querySelector<HTMLElement>("[data-modal-panel]");
    target?.focus({ preventScroll: true });
  }

  onMount(() => {
    const root = dialog;
    if (!root) return;
    const opener = document.activeElement;
    if (typeof root.showModal === "function") {
      if (!root.open) root.showModal();
    } else {
      root.setAttribute("open", "");
    }
    initialFocus(root);

    return () => {
      closing = true;
      if (root.open && typeof root.close === "function") root.close();
      // The dialog is removed from the document by its owner, which would
      // drop focus to <body>: give it back to whatever opened the dialog.
      if (
        opener instanceof HTMLElement &&
        opener !== document.body &&
        opener.isConnected
      ) {
        opener.focus({ preventScroll: true });
      }
    };
  });

  /**
   * Escape: the browser sends `cancel` to the top-most modal dialog only.
   * The owner decides (state lives in the page), so the native close is held
   * back and `onClose` runs once.
   */
  function handleCancel(event: Event) {
    event.preventDefault();
    if (!closing) onClose();
  }

  /**
   * A close the browser performed anyway (a repeated Escape fires a
   * non-cancelable `cancel`): tell the owner. An owner that keeps the dialog
   * (work in flight) still renders it, so it opens again instead of leaving
   * its later result inside a closed dialog.
   */
  function handleNativeClose() {
    if (closing) return;
    onClose();
    queueMicrotask(() => {
      if (
        !closing &&
        dialog?.isConnected &&
        !dialog.open &&
        typeof dialog.showModal === "function"
      ) {
        dialog.showModal();
      }
    });
  }

  // A click only dismisses when it began and ended on the backdrop, so
  // selecting text inside the panel and releasing outside does not close it.
  let pressStartedOnBackdrop = false;

  function handlePointerDown(event: PointerEvent) {
    pressStartedOnBackdrop = event.target === dialog;
  }

  function handleClick(event: MouseEvent) {
    if (pressStartedOnBackdrop && event.target === dialog) onClose();
    pressStartedOnBackdrop = false;
  }
</script>

<!-- The keyboard path is the dialog's native Escape/cancel behaviour; the click
     only handles the backdrop. -->
<dialog
  bind:this={dialog}
  class="kx-modal m-auto w-[calc(100%-2rem)] bg-transparent p-0 text-foreground {maxWidthClasses[
    maxWidth
  ]}"
  {role}
  aria-modal="true"
  aria-labelledby={title ? titleId : undefined}
  aria-label={title ? undefined : ariaLabel}
  aria-describedby={description ? descriptionId : undefined}
  data-testid={testId}
  oncancel={handleCancel}
  onclose={handleNativeClose}
  onpointerdown={handlePointerDown}
  onclick={handleClick}
>
  <div
    data-modal-panel
    tabindex="-1"
    class="bg-card rounded-xl border border-border shadow-2xl w-full max-h-[90vh] overflow-hidden flex flex-col outline-none"
  >
    <!-- Header -->
    {#if title || showClose}
      <div
        class="flex items-start justify-between p-4 sm:p-6 border-b border-border"
      >
        <div class="flex-1 min-w-0 pr-4">
          {#if title}
            <h2
              id={titleId}
              class="text-lg font-semibold break-words {tone === 'danger'
                ? 'text-destructive'
                : 'text-foreground'}"
            >
              {title}
            </h2>
          {/if}
          {#if description}
            <p id={descriptionId} class="text-sm text-muted-foreground mt-1">
              {description}
            </p>
          {/if}
        </div>
        {#if showClose}
          <button
            type="button"
            onclick={onClose}
            class="p-2 -m-2 rounded-lg text-muted-foreground hover:text-foreground hover:bg-muted transition-colors shrink-0"
            aria-label={tr("ui.common.close")}
          >
            <X class="w-5 h-5" />
          </button>
        {/if}
      </div>
    {/if}

    <!-- Content -->
    <div
      data-modal-content
      class="flex-1 overflow-y-auto overscroll-contain p-4 sm:p-6"
    >
      {@render children()}
    </div>

    <!-- Footer (optional) -->
    {#if footer}
      <div
        data-modal-footer
        class="flex flex-wrap items-center justify-end gap-3 p-4 sm:p-6 border-t border-border bg-muted/30"
      >
        {@render footer()}
      </div>
    {/if}
  </div>
</dialog>

<style>
  .kx-modal {
    border: 0;
    max-height: none;
    overflow: visible;
  }
  .kx-modal::backdrop {
    background: rgb(0 0 0 / 0.7);
    backdrop-filter: blur(4px);
  }
  @media (prefers-reduced-motion: no-preference) {
    .kx-modal[open] {
      animation: kx-modal-in 160ms ease-out;
    }
    .kx-modal[open]::backdrop {
      animation: kx-modal-backdrop-in 160ms ease-out;
    }
  }
  @keyframes kx-modal-in {
    from {
      opacity: 0;
      transform: translateY(12px);
    }
    to {
      opacity: 1;
      transform: none;
    }
  }
  @keyframes kx-modal-backdrop-in {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }
</style>
