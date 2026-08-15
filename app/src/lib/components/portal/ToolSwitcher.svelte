<script lang="ts">
  /**
   * ToolSwitcher - Dropdown/modal for switching between tool instances
   * Shows all available tools with status and quick switch
   */
  import StatusBadge from "$lib/components/monitoring/StatusBadge.svelte";
  import { fly, fade } from "svelte/transition";
  import { backOut } from "svelte/easing";

  type Status =
    | "operational"
    | "degraded"
    | "partial"
    | "down"
    | "maintenance"
    | "unknown";

  interface Tool {
    id: string;
    name: string;
    icon: string;
    brandColor: string;
    status: Status;
    description?: string;
  }

  interface Props {
    tools: Tool[];
    activeTool?: string;
    onSelect?: (toolId: string) => void;
    class?: string;
  }

  let {
    tools = [],
    activeTool,
    onSelect,
    class: className = "",
  }: Props = $props();

  let isOpen = $state(false);

  let currentTool = $derived(
    tools.find((t) => t.id === activeTool) || tools[0],
  );
</script>

<div class="tool-switcher relative {className}">
  <!-- Trigger Button -->
  <button
    onclick={() => (isOpen = !isOpen)}
    class="flex items-center gap-3 px-3 py-2 rounded-lg border border-border bg-card hover:bg-muted transition-colors w-full"
  >
    {#if currentTool}
      <div
        class="w-8 h-8 rounded-lg flex items-center justify-center text-lg"
        style="background: color-mix(in oklch, {currentTool.brandColor} 15%, transparent);"
      >
        {currentTool.icon}
      </div>
      <div class="flex-1 text-left">
        <div class="text-sm font-medium text-foreground">
          {currentTool.name}
        </div>
        <div class="text-xs text-muted-foreground">Active tool</div>
      </div>
    {/if}
    <svg
      class="w-4 h-4 text-muted-foreground transition-transform duration-200"
      class:rotate-180={isOpen}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="2"
    >
      <polyline points="6 9 12 15 18 9" />
    </svg>
  </button>

  <!-- Dropdown -->
  {#if isOpen}
    <div
      class="absolute z-50 top-full left-0 right-0 mt-2 p-2 rounded-xl border border-border bg-popover shadow-lg"
      in:fly={{ y: -10, duration: 200, easing: backOut }}
      out:fade={{ duration: 100 }}
    >
      <div
        class="text-xs font-medium text-muted-foreground uppercase tracking-wider px-2 py-1.5"
      >
        Switch Tool
      </div>

      <ul class="space-y-1">
        {#each tools as tool}
          <li>
            <button
              onclick={() => {
                onSelect?.(tool.id);
                isOpen = false;
              }}
              class="w-full flex items-center gap-3 px-2 py-2 rounded-lg transition-colors {tool.id ===
              activeTool
                ? 'bg-primary/10'
                : 'hover:bg-muted'}"
            >
              <div
                class="w-8 h-8 rounded-lg flex items-center justify-center text-lg"
                style="background: color-mix(in oklch, {tool.brandColor} 15%, transparent);"
              >
                {tool.icon}
              </div>
              <div class="flex-1 text-left">
                <div class="text-sm font-medium text-foreground">
                  {tool.name}
                </div>
                {#if tool.description}
                  <div class="text-xs text-muted-foreground">
                    {tool.description}
                  </div>
                {/if}
              </div>
              <StatusBadge status={tool.status} size="sm" showPulse={false} />
            </button>
          </li>
        {/each}
      </ul>
    </div>
  {/if}
</div>

<!-- Click outside to close -->
{#if isOpen}
  <button
    class="fixed inset-0 z-40"
    onclick={() => (isOpen = false)}
    aria-label="Close menu"
  ></button>
{/if}
