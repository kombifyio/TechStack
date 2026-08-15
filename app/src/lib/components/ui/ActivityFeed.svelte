<script lang="ts">
  /**
   * ActivityFeed - Real-time activity stream for monitoring
   * Shows recent events across all instances with live updates
   */
  import { fly, fade } from "svelte/transition";
  import { flip } from "svelte/animate";
  import { backOut } from "svelte/easing";

  type EventType = "info" | "success" | "warning" | "error" | "deploy" | "sync";

  export interface ActivityEvent {
    id: string;
    type: EventType;
    title: string;
    description?: string;
    source: string;
    timestamp: Date | string;
  }

  interface Props {
    events: ActivityEvent[];
    maxVisible?: number;
    showTimestamp?: boolean;
    class?: string;
  }

  let {
    events = [],
    maxVisible = 5,
    showTimestamp = true,
    class: className = "",
  }: Props = $props();

  let displayEvents = $derived(events.slice(0, maxVisible));

  const typeConfig: Record<
    EventType,
    { icon: string; color: string; bg: string }
  > = {
    info: { icon: "i", color: "text-info", bg: "bg-info/10" },
    success: { icon: "✓", color: "text-success", bg: "bg-success/10" },
    warning: { icon: "!", color: "text-warning", bg: "bg-warning/10" },
    error: { icon: "✕", color: "text-destructive", bg: "bg-destructive/10" },
    deploy: { icon: "▶", color: "text-primary", bg: "bg-primary/10" },
    sync: { icon: "↻", color: "text-primary", bg: "bg-primary/10" },
  };

  function formatTime(timestamp: Date | string): string {
    const date = timestamp instanceof Date ? timestamp : new Date(timestamp);
    const now = new Date();
    const diff = now.getTime() - date.getTime();

    if (diff < 60000) return "Just now";
    if (diff < 3600000) return `${Math.floor(diff / 60000)}m ago`;
    if (diff < 86400000) return `${Math.floor(diff / 3600000)}h ago`;
    return date.toLocaleDateString();
  }
</script>

<div class="activity-feed {className}">
  <div class="space-y-2">
    {#each displayEvents as event (event.id)}
      {@const config = typeConfig[event.type] || typeConfig.info}
      <div
        class="flex items-start gap-3 p-3 rounded-lg border border-border bg-card/50 hover:bg-card transition-colors"
        in:fly={{ x: -20, duration: 300, easing: backOut }}
        out:fade={{ duration: 150 }}
        animate:flip={{ duration: 250 }}
      >
        <!-- Icon -->
        <div
          class="flex-shrink-0 w-8 h-8 rounded-full flex items-center justify-center text-sm {config.bg} {config.color}"
        >
          {config.icon}
        </div>

        <!-- Content -->
        <div class="flex-1 min-w-0">
          <div class="flex items-start justify-between gap-2">
            <div>
              <p class="text-sm font-medium text-foreground">{event.title}</p>
              {#if event.description}
                <p class="text-xs text-muted-foreground mt-0.5 line-clamp-1">
                  {event.description}
                </p>
              {/if}
            </div>
            {#if showTimestamp}
              <span class="flex-shrink-0 text-xs text-muted-foreground">
                {formatTime(event.timestamp)}
              </span>
            {/if}
          </div>
          <div class="mt-1">
            <span
              class="inline-flex items-center text-xs text-muted-foreground"
            >
              <span
                class="w-1.5 h-1.5 rounded-full bg-muted-foreground/50 mr-1.5"
              ></span>
              {event.source}
            </span>
          </div>
        </div>
      </div>
    {/each}
  </div>

  {#if events.length > maxVisible}
    <div class="text-center mt-3">
      <button class="text-sm text-primary hover:underline">
        View all {events.length} events
      </button>
    </div>
  {/if}

  {#if events.length === 0}
    <div class="text-center py-8 text-muted-foreground">
      <div class="text-2xl mb-2 font-light">--</div>
      <p class="text-sm">No recent activity</p>
    </div>
  {/if}
</div>
