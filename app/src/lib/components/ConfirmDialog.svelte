<script lang="ts">
  /**
   * ConfirmDialog - Modern confirmation dialog
   * Uses Modal component with footer slot for actions
   */
  import Modal from "./Modal.svelte";
  import { AlertTriangle, Info, AlertCircle, Loader2 } from "@lucide/svelte";

  interface Props {
    title: string;
    message: string;
    confirmText?: string;
    cancelText?: string;
    confirmVariant?: "danger" | "primary" | "warning";
    onConfirm: () => void | Promise<void>;
    onCancel: () => void;
    loading?: boolean;
  }

  let {
    title,
    message,
    confirmText = "Confirm",
    cancelText = "Cancel",
    confirmVariant = "primary",
    onConfirm,
    onCancel,
    loading = false,
  }: Props = $props();

  const variantClasses = {
    danger:
      "bg-destructive hover:bg-destructive/90 text-destructive-foreground",
    primary: "bg-primary hover:bg-primary/90 text-primary-foreground",
    warning: "bg-amber-600 hover:bg-amber-500 text-white",
  };

  const iconVariants = {
    danger: AlertCircle,
    primary: Info,
    warning: AlertTriangle,
  };

  const iconColors = {
    danger: "text-destructive",
    primary: "text-primary",
    warning: "text-amber-500",
  };

  async function handleConfirm() {
    await onConfirm();
  }

  const Icon = $derived(iconVariants[confirmVariant]);
</script>

<Modal {title} onClose={onCancel} maxWidth="sm">
  <div class="flex gap-4">
    <div
      class="shrink-0 w-10 h-10 rounded-full flex items-center justify-center {confirmVariant ===
      'danger'
        ? 'bg-destructive/10'
        : confirmVariant === 'warning'
          ? 'bg-amber-500/10'
          : 'bg-primary/10'}"
    >
      <Icon class="w-5 h-5 {iconColors[confirmVariant]}" />
    </div>
    <div class="flex-1 min-w-0">
      <p class="text-muted-foreground">{message}</p>
    </div>
  </div>

  {#snippet footer()}
    <button
      type="button"
      onclick={onCancel}
      disabled={loading}
      class="px-4 py-2 rounded-lg bg-secondary text-secondary-foreground hover:bg-secondary/80 transition-colors disabled:opacity-50"
    >
      {cancelText}
    </button>
    <button
      type="button"
      onclick={handleConfirm}
      disabled={loading}
      class="px-4 py-2 rounded-lg {variantClasses[
        confirmVariant
      ]} transition-colors disabled:opacity-50 flex items-center gap-2"
    >
      {#if loading}
        <Loader2 class="w-4 h-4 animate-spin" />
      {/if}
      {confirmText}
    </button>
  {/snippet}
</Modal>
