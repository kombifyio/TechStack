<script lang="ts">
  /**
   * GroupedTaskList - Phase-grouped progress feedback for long-running
   * creation jobs. Each phase is one card with a status badge and a
   * collapsible drill-down of its sub-steps. The active phase auto-expands
   * so the user always sees what is happening right now without scrolling
   * past 8 instantly-completed Unifier steps.
   */
  import { fade, slide } from "svelte/transition";
  import type { Task } from "$lib/wizard";
  import { computeGroupStatuses, type GroupStatus } from "$lib/wizard";

  interface Props {
    tasks: Task[];
    class?: string;
  }

  let { tasks, class: className = "" }: Props = $props();

  let groupStatuses = $derived(computeGroupStatuses(tasks));
  let expanded = $state<Record<string, boolean>>({});

  function isAutoExpanded(gs: GroupStatus): boolean {
    return gs.status === "running" || gs.status === "failed";
  }

  function isExpanded(gs: GroupStatus): boolean {
    const manual = expanded[gs.group.id];
    if (manual !== undefined) return manual;
    return isAutoExpanded(gs);
  }

  function toggle(gs: GroupStatus) {
    expanded[gs.group.id] = !isExpanded(gs);
  }

  function badgeClass(status: Task["status"]): string {
    switch (status) {
      case "completed":
        return "bg-green-500/10 text-green-500";
      case "running":
        return "bg-primary/10 text-primary";
      case "failed":
        return "bg-red-500/10 text-red-500";
      default:
        return "bg-muted text-muted-foreground";
    }
  }

  function badgeLabel(gs: GroupStatus): string {
    switch (gs.status) {
      case "completed":
        return "Done";
      case "running":
        return `${gs.completed} / ${gs.total}`;
      case "failed":
        return "Failed";
      default:
        return `0 / ${gs.total}`;
    }
  }

  function subIconClass(status: Task["status"]): string {
    switch (status) {
      case "completed":
        return "text-green-500";
      case "running":
        return "text-primary";
      case "failed":
        return "text-red-500";
      default:
        return "text-muted-foreground/60";
    }
  }

  function subIcon(status: Task["status"]): string {
    switch (status) {
      case "completed":
        return "✓";
      case "running":
        return "◐";
      case "failed":
        return "✕";
      default:
        return "○";
    }
  }
</script>

<div class="grouped-task-list flex flex-col gap-3 {className}">
  {#each groupStatuses as gs (gs.group.id)}
    {@const open = isExpanded(gs)}
    {@const borderClass =
      gs.status === "failed"
        ? "border-red-500/40"
        : gs.status === "running"
          ? "border-primary/60"
          : gs.status === "completed"
            ? "border-border"
            : "border-border"}
    {@const opacityClass = gs.status === "completed" ? "opacity-70" : ""}
    <div
      data-testid="task-group"
      data-group-id={gs.group.id}
      data-status={gs.status}
      class="task-group-card relative overflow-hidden rounded-xl border bg-card transition-all duration-300 {borderClass} {opacityClass}"
    >
      {#if gs.status === "running"}
        <div
          class="absolute inset-x-0 top-0 h-0.5 bg-primary/30 overflow-hidden"
          in:fade={{ duration: 200 }}
        >
          <div class="h-full w-1/3 bg-primary animate-progress-slide"></div>
        </div>
      {/if}

      <button
        type="button"
        onclick={() => toggle(gs)}
        class="w-full flex items-start gap-3 p-4 text-left hover:bg-muted/30 transition-colors"
        aria-expanded={open}
        aria-controls={`group-${gs.group.id}-details`}
      >
        <div
          class="shrink-0 w-9 h-9 rounded-full flex items-center justify-center font-bold text-base {gs.status ===
          'running'
            ? 'bg-primary/10 animate-pulse'
            : gs.status === 'failed'
              ? 'bg-red-500/10'
              : gs.status === 'completed'
                ? 'bg-green-500/10'
                : 'bg-muted'} {subIconClass(gs.status)}"
        >
          {#if gs.status === "running"}
            <svg
              class="w-4 h-4 animate-spin"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
            >
              <circle cx="12" cy="12" r="10" stroke-opacity="0.25" />
              <path d="M12 2a10 10 0 0 1 10 10" stroke-linecap="round" />
            </svg>
          {:else}
            <span class="text-sm">{subIcon(gs.status)}</span>
          {/if}
        </div>

        <div class="flex-1 min-w-0">
          <div class="flex items-center gap-2 mb-0.5">
            <h4
              class="font-medium text-sm leading-tight"
              class:text-foreground={gs.status !== "pending"}
              class:text-muted-foreground={gs.status === "pending"}
            >
              {gs.group.label}
            </h4>
            <span
              class="shrink-0 text-[10px] font-medium px-2 py-0.5 rounded-full {badgeClass(
                gs.status,
              )}"
            >
              {badgeLabel(gs)}
            </span>
          </div>
          {#if gs.status === "running" && gs.runningTask}
            <p class="text-xs text-primary/90 truncate">
              {gs.runningTask.message || gs.runningTask.label}
            </p>
          {:else if gs.status === "failed" && gs.failedTask?.errorMessage}
            <p class="text-xs text-red-400 truncate">
              {gs.failedTask.errorMessage}
            </p>
          {:else}
            <p class="text-xs text-muted-foreground truncate">
              {gs.group.description}
            </p>
          {/if}
        </div>

        <svg
          class="shrink-0 w-4 h-4 text-muted-foreground transition-transform mt-1 {open
            ? 'rotate-180'
            : ''}"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
          stroke-width="2"
          aria-hidden="true"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            d="M19 9l-7 7-7-7"
          />
        </svg>
      </button>

      {#if open}
        <div
          id={`group-${gs.group.id}-details`}
          class="border-t border-border/60 bg-background/40"
          transition:slide={{ duration: 200 }}
        >
          <ul class="px-4 py-3 space-y-1.5">
            {#each gs.tasks as sub (sub.id)}
              <li
                data-testid="task-subitem"
                data-task-id={sub.id}
                data-status={sub.status}
                class="flex items-center gap-2 text-xs"
              >
                <span
                  class="shrink-0 w-4 text-center font-bold {subIconClass(
                    sub.status,
                  )}"
                >
                  {sub.status === "running" ? "◐" : subIcon(sub.status)}
                </span>
                <span
                  class="flex-1 truncate"
                  class:text-foreground={sub.status === "running" ||
                    sub.status === "failed"}
                  class:text-muted-foreground={sub.status !== "running" &&
                    sub.status !== "failed"}
                >
                  {sub.label}
                </span>
              </li>
            {/each}
          </ul>
        </div>
      {/if}
    </div>
  {/each}
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

  .task-group-card {
    will-change: transform, opacity;
  }
</style>
