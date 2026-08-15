<script lang="ts">
  /**
   * Toast - Modern notification component
   * Uses CSS variables and lucide-svelte icons
   */
  import { toasts, type Toast } from "$lib/stores/toast";
  import { fly, fade } from "svelte/transition";
  import { CheckCircle, XCircle, AlertTriangle, Info, X } from "@lucide/svelte";

  const iconMap = {
    success: CheckCircle,
    error: XCircle,
    warning: AlertTriangle,
    info: Info,
  };

  const colorMap = {
    success: "bg-emerald-600/90 border-emerald-500/50",
    error: "bg-destructive/90 border-destructive/50",
    warning: "bg-amber-600/90 border-amber-500/50",
    info: "bg-primary/90 border-primary/50",
  };

  const iconColors = {
    success: "text-emerald-200",
    error: "text-red-200",
    warning: "text-amber-200",
    info: "text-primary",
  };
</script>

<div
  class="fixed top-4 right-4 z-[100] flex flex-col gap-2 max-w-md pointer-events-none"
>
  {#each $toasts as toast (toast.id)}
    {@const Icon = iconMap[toast.type]}
    <div
      class="flex items-start gap-3 p-4 rounded-xl border shadow-2xl backdrop-blur-sm text-white pointer-events-auto {colorMap[
        toast.type
      ]}"
      in:fly={{ x: 100, duration: 250, delay: 50 }}
      out:fade={{ duration: 150 }}
    >
      <Icon class="w-5 h-5 shrink-0 mt-0.5 {iconColors[toast.type]}" />
      <div class="flex-1 min-w-0">
        <p class="font-medium leading-tight">{toast.message}</p>
        {#if toast.details}
          <p class="text-sm opacity-80 mt-1 break-words">{toast.details}</p>
        {/if}
      </div>
      <button
        class="text-white/60 hover:text-white transition-colors rounded-md p-0.5 hover:bg-white/10 -mr-1 -mt-1"
        onclick={() => toasts.remove(toast.id)}
        aria-label="Dismiss notification"
      >
        <X class="w-4 h-4" />
      </button>
    </div>
  {/each}
</div>
