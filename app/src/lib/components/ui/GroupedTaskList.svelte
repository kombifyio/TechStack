<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  /**
   * GroupedTaskList - Phase-grouped progress feedback for long-running
   * creation jobs. Each phase is one card with a status badge and a
   * collapsible drill-down of its sub-steps. The active phase auto-expands
   * so the user always sees what is happening right now without scrolling
   * past 8 instantly-completed Unifier steps.
   *
   * Focused mode turns the list into a reel: the active phase sits in the
   * middle, finished phases travel upward and both edges fade out. The reel
   * scrolls natively; it re-centres on the active phase unless the user is
   * browsing it.
   */
  import { onDestroy, tick, type Snippet } from "svelte";
  import { fade, slide } from "svelte/transition";
  import type { Task, TaskTimeline } from "#lib/wizard/index.js";
  import {
    computeGroupStatuses,
    phaseTiming,
    type GroupStatus,
    type PhaseTiming,
  } from "#lib/wizard/index.js";

  interface Props {
    tasks: Task[];
    /** With a timeline and a ticking clock, each phase shows its timer. */
    timeline?: TaskTimeline;
    now?: number;
    class?: string;
    /** Render as a reel that keeps the active phase centred. */
    focused?: boolean;
    /** Extra detail shown inside the active phase card in focused mode. */
    activeDetail?: Snippet;
  }

  let {
    tasks,
    timeline,
    now,
    class: className = "",
    focused = false,
    activeDetail,
  }: Props = $props();

  let groupStatuses = $derived(computeGroupStatuses(tasks));

  // The phase the reel centres: the running one, else a failed one, else
  // the next one ahead, else the last one.
  let activeIndex = $derived.by(() => {
    const running = groupStatuses.findIndex((gs) => gs.status === "running");
    if (running >= 0) return running;
    const failed = groupStatuses.findIndex((gs) => gs.status === "failed");
    if (failed >= 0) return failed;
    const next = groupStatuses.findIndex((gs) => gs.status === "pending");
    return next >= 0 ? next : groupStatuses.length - 1;
  });

  let viewport = $state<HTMLDivElement>();
  let track = $state<HTMLDivElement>();
  let slots: HTMLDivElement[] = $state([]);
  // Set while the user scrolls the reel themselves; auto-centring waits.
  let browsing = false;
  let hovered = false;
  let returnTimer: ReturnType<typeof setTimeout> | undefined;
  let depthFrame = 0;

  function reducedMotion(): boolean {
    return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  }

  // Each card fades and recedes with its distance from the reel centre.
  function updateDepth() {
    depthFrame = 0;
    if (!viewport) return;
    const half = viewport.clientHeight / 2;
    const centre = viewport.scrollTop + half;
    for (const slot of slots) {
      if (!slot) continue;
      const mid = slot.offsetTop + slot.offsetHeight / 2;
      const t = Math.min(1, Math.abs(mid - centre) / half);
      slot.style.setProperty("--reel-t", t.toFixed(3));
    }
  }

  function scheduleDepth() {
    if (!depthFrame) depthFrame = requestAnimationFrame(updateDepth);
  }

  function centreActive(smooth = true) {
    const slot = slots[activeIndex];
    if (!viewport || !slot) return;
    const top =
      slot.offsetTop + slot.offsetHeight / 2 - viewport.clientHeight / 2;
    viewport.scrollTo({
      top,
      behavior: smooth && !reducedMotion() ? "smooth" : "auto",
    });
    scheduleDepth();
  }

  function returnLater(delay: number) {
    clearTimeout(returnTimer);
    returnTimer = setTimeout(() => {
      if (hovered) return;
      browsing = false;
      centreActive();
    }, delay);
  }

  function startBrowsing() {
    browsing = true;
    returnLater(6000);
  }

  function onFocusIn(event: FocusEvent) {
    const index = slots.findIndex((slot) => slot?.contains(event.target as Node));
    if (index >= 0 && index !== activeIndex) startBrowsing();
  }

  // Re-centre when the active phase changes.
  $effect(() => {
    if (!focused) return;
    void activeIndex;
    void tick().then(() => {
      if (!browsing) centreActive();
    });
  });

  // Cards grow and shrink (drill-downs, status text); keep the centre.
  $effect(() => {
    if (!focused || !track) return;
    let first = true;
    const observer = new ResizeObserver(() => {
      if (!browsing) centreActive(!first);
      first = false;
      scheduleDepth();
    });
    observer.observe(track);
    if (viewport) observer.observe(viewport);
    return () => observer.disconnect();
  });

  onDestroy(() => {
    clearTimeout(returnTimer);
    if (depthFrame) cancelAnimationFrame(depthFrame);
  });

  // A phase shows its typical duration while ahead, a running timer while
  // active, and its observed duration once done (none for instant phases).
  function timerFor(gs: GroupStatus): PhaseTiming | null {
    if (!timeline || now === undefined || gs.status === "failed") return null;
    const timing = phaseTiming(gs, timeline, now);
    if (gs.status === "completed" && !timing.elapsedSeconds) return null;
    return timing;
  }

  function formatElapsed(seconds: number): string {
    const minutes = Math.floor(seconds / 60);
    return `${minutes}:${String(seconds % 60).padStart(2, "0")}`;
  }

  function formatTypical(seconds: number): string {
    return seconds < 60
      ? tr("ui.groupedTaskList.aboutSeconds", { count: seconds })
      : tr("ui.groupedTaskList.aboutMinutes", {
          count: Math.round(seconds / 60),
        });
  }
  let expanded = $state<Record<string, boolean>>({});

  function isAutoExpanded(gs: GroupStatus): boolean {
    if (focused && activeDetail && gs === groupStatuses[activeIndex]) return true;
    return gs.status === "running" || gs.status === "failed" || gs.attention;
  }

  function isExpanded(gs: GroupStatus): boolean {
    const manual = expanded[gs.group.id];
    if (manual !== undefined) return manual;
    return isAutoExpanded(gs);
  }

  function toggle(gs: GroupStatus) {
    expanded[gs.group.id] = !isExpanded(gs);
  }

  // Completed with a degraded part: amber, never the failed red.
  function badgeClass(status: Task["status"], attention = false): string {
    if (attention) return "bg-warning/10 text-warning";
    switch (status) {
      case "completed":
        return "bg-success/10 text-success";
      case "running":
        return "bg-primary/10 text-primary";
      case "failed":
        return "bg-destructive/10 text-destructive";
      default:
        return "bg-muted text-muted-foreground";
    }
  }

  function badgeLabel(gs: GroupStatus): string {
    if (gs.attention) return tr("ui.groupedTaskList.needsAttention");
    switch (gs.status) {
      case "completed":
        return tr("ui.groupedTaskList.done");
      case "running":
        return `${gs.completed} / ${gs.total}`;
      case "failed":
        return tr("ui.stacksCreatingCreationRunStatus.failed");
      default:
        return `0 / ${gs.total}`;
    }
  }

  function subIconClass(status: Task["status"], attention = false): string {
    if (attention) return "text-warning";
    switch (status) {
      case "completed":
        return "text-success";
      case "running":
        return "text-primary";
      case "failed":
        return "text-destructive";
      default:
        return "text-muted-foreground/60";
    }
  }

  function subIcon(status: Task["status"], attention = false): string {
    if (attention) return "!";
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

{#snippet phaseCard(gs: GroupStatus, active: boolean)}
  {@const open = isExpanded(gs)}
  {@const timing = timerFor(gs)}
  {@const borderClass = gs.attention
    ? "border-warning/50"
    : gs.status === "failed"
      ? "border-destructive/40"
      : gs.status === "running"
        ? "border-primary/60"
        : gs.status === "completed"
          ? "border-border"
          : "border-border"}
  {@const opacityClass =
    gs.status === "completed" && !gs.attention && !active ? "opacity-70" : ""}
  {@const detail = active && activeDetail && gs.status !== "failed"}
  <div
    data-testid="task-group"
    data-group-id={gs.group.id}
    data-status={gs.status}
    data-attention={gs.attention || undefined}
    class="task-group-card relative overflow-hidden rounded-xl border bg-card {borderClass} {opacityClass}"
    class:task-group-card-active={active}
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
            ? 'bg-destructive/10'
            : gs.attention
              ? 'bg-warning/10'
              : gs.status === 'completed'
                ? 'bg-success/10'
                : 'bg-muted'} {subIconClass(gs.status, gs.attention)}"
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
          <span class="text-sm">{subIcon(gs.status, gs.attention)}</span>
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
              gs.attention,
            )}"
          >
            {badgeLabel(gs)}
          </span>
        </div>
        {#if detail}
          <p class="text-xs text-muted-foreground truncate">
            {gs.group.description}
          </p>
        {:else if gs.status === "running"}
          <p class="text-xs text-primary/90 truncate">
            {gs.runningTask?.message ||
              gs.runningTask?.label ||
              gs.group.description}
          </p>
        {:else if gs.status === "failed" && gs.failedTask?.errorMessage}
          <p class="text-xs text-destructive truncate">
            {gs.failedTask.errorMessage}
          </p>
        {:else}
          <p class="text-xs text-muted-foreground truncate">
            {gs.group.description}
          </p>
        {/if}
        {#if timing}
          {@const elapsed = timing.elapsedSeconds ?? 0}
          <p
            class="mt-1 flex items-center gap-1.5 text-[11px] text-muted-foreground"
            data-testid="task-group-timer"
          >
            {#if timing.elapsedSeconds !== undefined}
              <span
                class="font-mono tabular-nums"
                class:text-primary={gs.status === "running"}
                aria-hidden="true"
                >{formatElapsed(elapsed)}</span
              >
            {/if}
            {#if gs.status === "running"}
              <span aria-hidden="true">·</span>
            {/if}
            {#if gs.status !== "completed"}
              <span>
                {gs.status === "running" &&
                elapsed > timing.typicalSeconds * 1.5
                  ? tr("ui.groupedTaskList.longerThanUsual")
                  : tr("ui.groupedTaskList.usually", {
                      duration: formatTypical(timing.typicalSeconds),
                    })}
              </span>
            {/if}
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
        {#if detail}
          <div class="px-4 pt-4" aria-live="polite">
            {@render activeDetail?.()}
          </div>
        {/if}
        <ul class="px-4 py-3 space-y-1.5">
          {#each gs.tasks as sub (sub.id)}
            <li
              data-testid="task-subitem"
              data-task-id={sub.id}
              data-status={sub.status}
              data-attention={sub.attention || undefined}
              class="flex items-center gap-2 text-xs"
            >
              <span
                class="shrink-0 w-4 text-center font-bold {subIconClass(
                  sub.status,
                  sub.attention,
                )}"
              >
                {sub.status === "running"
                  ? "◐"
                  : subIcon(sub.status, sub.attention)}
              </span>
              <span
                class="flex-1 truncate"
                class:text-foreground={sub.status === "running" ||
                  sub.status === "failed" ||
                  sub.attention}
                class:text-muted-foreground={sub.status !== "running" &&
                  sub.status !== "failed" &&
                  !sub.attention}
              >
                {sub.label}
              </span>
            </li>
          {/each}
        </ul>
      </div>
    {/if}
  </div>
{/snippet}

{#if focused}
  <!-- The listeners only notice that the user browses the reel; every
       action stays on the phase buttons. -->
  <div
    bind:this={viewport}
    class="task-reel {className}"
    data-testid="task-reel"
    role="region"
    aria-label={tr("ui.stacksCreatingCreationProgress.progress")}
    onscroll={scheduleDepth}
    onwheel={startBrowsing}
    ontouchmove={startBrowsing}
    onfocusin={onFocusIn}
    onpointerenter={() => (hovered = true)}
    onpointerleave={() => {
      hovered = false;
      if (browsing) returnLater(2500);
    }}
  >
    <div class="task-reel-spacer" aria-hidden="true"></div>
    <div bind:this={track} class="grouped-task-list flex flex-col gap-3">
      {#each groupStatuses as gs, i (gs.group.id)}
        <div
          bind:this={slots[i]}
          class="task-reel-slot"
          data-active={i === activeIndex || undefined}
        >
          {@render phaseCard(gs, i === activeIndex)}
        </div>
      {/each}
    </div>
    <div class="task-reel-spacer" aria-hidden="true"></div>
  </div>
{:else}
  <div class="grouped-task-list flex flex-col gap-3 {className}">
    {#each groupStatuses as gs (gs.group.id)}
      {@render phaseCard(gs, false)}
    {/each}
  </div>
{/if}

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
    transition:
      border-color var(--kx-dur-fast) var(--kx-ease-out),
      opacity var(--kx-dur-slow) var(--kx-ease-out),
      box-shadow var(--kx-dur-slow) var(--kx-ease-out);
  }

  .task-group-card-active {
    box-shadow: 0 18px 40px -24px
      color-mix(in oklab, var(--primary) 45%, transparent);
  }

  /* The reel: a fixed window whose edges dissolve like fog. Spacers of half
     the window let the first and last phase reach the centre. */
  .task-reel {
    position: relative;
    height: var(--task-reel-height, clamp(26rem, 62vh, 38rem));
    overflow-y: auto;
    scrollbar-width: none;
    padding-inline: 0.75rem;
    margin-inline: -0.75rem;
    mask-image: linear-gradient(
      to bottom,
      transparent 0,
      #000 14%,
      #000 86%,
      transparent 100%
    );
  }

  .task-reel::-webkit-scrollbar {
    display: none;
  }

  .task-reel-spacer {
    height: 50%;
  }

  .task-reel-slot {
    opacity: calc(1 - var(--reel-t, 0) * 0.45);
    transition: opacity var(--kx-dur-fast) var(--kx-ease-out);
  }

  @media (prefers-reduced-motion: no-preference) {
    .task-reel-slot {
      transform: scale(calc(1 - var(--reel-t, 0) * 0.05));
      filter: blur(calc(max(0, var(--reel-t, 0) - 0.7) * 4px));
      will-change: transform, opacity;
    }
  }
</style>
