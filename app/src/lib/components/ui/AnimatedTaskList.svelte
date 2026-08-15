<script lang="ts">
  /**
   * AnimatedTaskList - Progress feedback component for long-running operations
   * Inspired by: https://magicui.design/docs/components/animated-list
   *
   * Shows a list of tasks that animate in sequence, with the current task
   * prominently displayed and completed tasks sliding up.
   *
   * Adapted for kombify-TechStack task status types
   */
  import { flip } from "svelte/animate";
  import { fly, fade, scale } from "svelte/transition";
  import { backOut, cubicOut } from "svelte/easing";

  export interface TaskItem {
    id: string;
    title: string;
    description?: string;
    status:
      | "pending"
      | "running"
      | "in-progress"
      | "completed"
      | "failed"
      | "error";
    message?: string;
    icon?: string;
  }

  interface Props {
    tasks: TaskItem[];
    currentTaskIndex?: number;
    showCompleted?: boolean;
    maxVisible?: number;
    compact?: boolean;
    class?: string;
  }

  let {
    tasks = [],
    currentTaskIndex = 0,
    showCompleted = true,
    maxVisible = 6,
    compact = false,
    class: className = "",
  }: Props = $props();

  // Show all tasks when showCompleted is true (no truncation)
  let displayTasks = $derived(() => {
    if (showCompleted) return tasks;
    const filtered = tasks.filter((t) => t.status !== "completed");
    return maxVisible > 0 ? filtered.slice(0, maxVisible) : filtered;
  });

  // Normalize status for consistent handling
  function normalizeStatus(
    status: TaskItem["status"],
  ): "pending" | "in-progress" | "completed" | "error" {
    if (status === "running") return "in-progress";
    if (status === "failed") return "error";
    return status as "pending" | "in-progress" | "completed" | "error";
  }

  function getStatusIcon(status: TaskItem["status"]) {
    const normalized = normalizeStatus(status);
    switch (normalized) {
      case "pending":
        return "○";
      case "in-progress":
        return "◐";
      case "completed":
        return "✓";
      case "error":
        return "✕";
      default:
        return "○";
    }
  }

  function getStatusColor(status: TaskItem["status"]) {
    const normalized = normalizeStatus(status);
    switch (normalized) {
      case "pending":
        return "text-muted-foreground";
      case "in-progress":
        return "text-primary";
      case "completed":
        return "text-green-500";
      case "error":
        return "text-red-500";
      default:
        return "text-muted-foreground";
    }
  }

  function isInProgress(status: TaskItem["status"]) {
    return status === "running" || status === "in-progress";
  }
</script>

<div class="animated-task-list flex flex-col gap-3 {className}">
  {#each displayTasks() as task, index (task.id)}
    {@const normalized = normalizeStatus(task.status)}
    {@const borderClass =
      normalized === "error"
        ? "border-red-500/50"
        : isInProgress(task.status)
          ? "border-primary"
          : "border-border"}
    {@const bgClass = normalized === "error" ? "bg-red-500/5" : ""}
    {@const opacityClass =
      normalized === "completed" ? "opacity-60 scale-[0.98]" : ""}
    <div
      data-testid="task-item"
      data-task-id={task.id}
      data-status={normalized}
      class="task-card relative overflow-hidden rounded-xl border bg-card transition-all duration-300 {compact
        ? 'p-3'
        : 'p-4'} {borderClass} {bgClass} {opacityClass}"
      in:fly={{ y: 50, duration: 400, easing: backOut }}
      out:fly={{ y: -30, duration: 300, easing: cubicOut }}
      animate:flip={{ duration: 300 }}
    >
      <!-- Progress indicator for in-progress -->
      {#if isInProgress(task.status)}
        <div
          class="absolute inset-x-0 top-0 h-0.5 bg-primary/30 overflow-hidden"
          in:fade={{ duration: 200 }}
        >
          <div class="h-full w-1/3 bg-primary animate-progress-slide"></div>
        </div>
      {/if}

      <div class="flex items-start gap-4">
        <!-- Status Icon -->
        <div
          class="shrink-0 rounded-full flex items-center justify-center text-lg font-bold transition-colors duration-300 {getStatusColor(
            task.status,
          )} {isInProgress(task.status)
            ? 'bg-primary/10 animate-pulse'
            : normalized === 'error'
              ? 'bg-red-500/10'
              : 'bg-muted'} {compact ? 'w-8 h-8' : 'w-10 h-10'}"
        >
          {#if isInProgress(task.status)}
            <svg
              class="w-5 h-5 animate-spin"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
            >
              <circle cx="12" cy="12" r="10" stroke-opacity="0.25" />
              <path d="M12 2a10 10 0 0 1 10 10" stroke-linecap="round" />
            </svg>
          {:else}
            <span class="text-sm">{getStatusIcon(task.status)}</span>
          {/if}
        </div>

        <!-- Content -->
        <div class="flex-1 min-w-0">
          <h4
            class="font-medium transition-colors duration-300 {compact
              ? 'text-sm'
              : ''}"
            class:text-foreground={isInProgress(task.status)}
            class:text-red-400={normalized === "error"}
            class:text-muted-foreground={!isInProgress(task.status) &&
              normalized !== "error"}
          >
            {task.title}
          </h4>
          {#if task.description && !compact}
            <p class="text-sm text-muted-foreground mt-1 line-clamp-2">
              {task.description}
            </p>
          {/if}
          {#if task.message}
            <p class="text-sm text-primary mt-1 animate-pulse line-clamp-1">
              {task.message}
            </p>
          {/if}
        </div>

        <!-- Status badge -->
        {#if normalized === "completed"}
          <span
            class="shrink-0 text-xs font-medium text-green-500 bg-green-500/10 px-2 py-1 rounded-full"
            in:scale={{ duration: 200, start: 0.8 }}
          >
            Done
          </span>
        {:else if normalized === "error"}
          <span
            class="shrink-0 text-xs font-medium text-red-500 bg-red-500/10 px-2 py-1 rounded-full"
          >
            Failed
          </span>
        {/if}
      </div>
    </div>
  {/each}

  <!-- Remaining tasks indicator (only shown when tasks are filtered) -->
  {#if !showCompleted && displayTasks().length < tasks.length}
    <div class="text-center text-sm text-muted-foreground py-2" in:fade>
      +{tasks.length - displayTasks().length} more tasks remaining
    </div>
  {/if}
</div>

<style>
  @keyframes progress-slide {
    0% {
      transform: translateX(-100%);
    }
    100% {
      transform: translateX(400%);
    }
  }

  .animate-progress-slide {
    animation: progress-slide 1.5s ease-in-out infinite;
  }

  .task-card {
    will-change: transform, opacity;
  }
</style>
