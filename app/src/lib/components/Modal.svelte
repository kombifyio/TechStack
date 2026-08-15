<script lang="ts">
  /**
   * Modal - Modern modal dialog component
   * Uses CSS variables for theming and smooth animations
   */
  import type { Snippet } from "svelte";
  import { fly, fade } from "svelte/transition";
  import { X } from "@lucide/svelte";

  interface Props {
    title?: string;
    description?: string;
    onClose: () => void;
    maxWidth?: "sm" | "md" | "lg" | "xl" | "2xl" | "3xl" | "full";
    showClose?: boolean;
    children: Snippet;
    footer?: Snippet;
  }

  let {
    title,
    description,
    onClose,
    maxWidth = "lg",
    showClose = true,
    children,
    footer,
  }: Props = $props();

  const maxWidthClasses: Record<string, string> = {
    sm: "max-w-sm",
    md: "max-w-md",
    lg: "max-w-lg",
    xl: "max-w-xl",
    "2xl": "max-w-2xl",
    "3xl": "max-w-3xl",
    full: "max-w-[95vw]",
  };

  function handleBackdropClick(e: MouseEvent) {
    if (e.target === e.currentTarget) {
      onClose();
    }
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === "Escape") {
      onClose();
    }
  }
</script>

<svelte:window onkeydown={handleKeydown} />

<!-- svelte-ignore a11y_click_events_have_key_events -->
<div
  class="fixed inset-0 bg-black/70 backdrop-blur-sm flex items-center justify-center z-50 p-4"
  onclick={handleBackdropClick}
  role="presentation"
  in:fade={{ duration: 150 }}
  out:fade={{ duration: 100 }}
>
  <div
    class="bg-card rounded-xl border border-border shadow-2xl {maxWidthClasses[
      maxWidth
    ]} w-full max-h-[90vh] overflow-hidden flex flex-col"
    role="dialog"
    aria-modal="true"
    aria-labelledby={title ? "modal-title" : undefined}
    aria-describedby={description ? "modal-description" : undefined}
    in:fly={{ y: 20, duration: 200 }}
    out:fly={{ y: 10, duration: 100 }}
  >
    <!-- Header -->
    {#if title || showClose}
      <div
        class="flex items-start justify-between p-4 sm:p-6 border-b border-border"
      >
        <div class="flex-1 min-w-0 pr-4">
          {#if title}
            <h2
              id="modal-title"
              class="text-lg font-semibold text-foreground truncate"
            >
              {title}
            </h2>
          {/if}
          {#if description}
            <p
              id="modal-description"
              class="text-sm text-muted-foreground mt-1"
            >
              {description}
            </p>
          {/if}
        </div>
        {#if showClose}
          <button
            onclick={onClose}
            class="p-2 -m-2 rounded-lg text-muted-foreground hover:text-foreground hover:bg-muted transition-colors shrink-0"
            aria-label="Close"
          >
            <X class="w-5 h-5" />
          </button>
        {/if}
      </div>
    {/if}

    <!-- Content -->
    <div class="flex-1 overflow-y-auto p-4 sm:p-6">
      {@render children()}
    </div>

    <!-- Footer (optional) -->
    {#if footer}
      <div
        class="flex items-center justify-end gap-3 p-4 sm:p-6 border-t border-border bg-muted/30"
      >
        {@render footer()}
      </div>
    {/if}
  </div>
</div>
