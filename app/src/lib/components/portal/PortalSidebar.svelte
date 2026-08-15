<script lang="ts">
  /**
   * PortalSidebar - Navigation sidebar for the unified portal
   * Provides navigation between tools and dashboard sections
   */
  import { fly } from "svelte/transition";

  interface NavItem {
    id: string;
    label: string;
    icon: string;
    badge?: string | number;
    href?: string;
  }

  interface NavGroup {
    title?: string;
    items: NavItem[];
  }

  interface Props {
    groups: NavGroup[];
    activeId?: string;
    collapsed?: boolean;
    onNavigate?: (id: string) => void;
    onToggleCollapse?: () => void;
    class?: string;
  }

  let {
    groups = [],
    activeId,
    collapsed = false,
    onNavigate,
    onToggleCollapse,
    class: className = "",
  }: Props = $props();
</script>

<aside
  class="portal-sidebar flex flex-col h-full border-r border-border bg-card/50 transition-all duration-300 {collapsed
    ? 'w-16'
    : 'w-max'} {className}"
>
  <!-- Logo/Header -->
  <div class="flex items-center gap-3 px-4 py-4 border-b border-border">
    <div
      class="w-8 h-8 rounded-lg bg-primary flex items-center justify-center text-primary-foreground font-bold text-sm flex-shrink-0"
    >
      K
    </div>
    {#if !collapsed}
      <div in:fly={{ x: -10, duration: 200 }}>
        <span class="font-semibold text-foreground">kombify</span>
        <span class="text-muted-foreground"> Stack</span>
      </div>
    {/if}
  </div>

  <!-- Navigation -->
  <nav class="flex-1 overflow-y-auto py-4 space-y-6">
    {#each groups as group}
      <div class="px-3">
        {#if group.title && !collapsed}
          <h4
            class="text-xs font-medium text-muted-foreground uppercase tracking-wider mb-2 px-2"
          >
            {group.title}
          </h4>
        {/if}

        <ul class="space-y-1">
          {#each group.items as item}
            <li>
              <button
                onclick={() => onNavigate?.(item.id)}
                class="w-full flex items-center gap-3 px-2 py-2 rounded-lg text-sm transition-colors {activeId ===
                item.id
                  ? 'bg-primary/10 text-primary font-medium'
                  : 'text-muted-foreground hover:text-foreground hover:bg-muted'}"
                title={collapsed ? item.label : undefined}
              >
                <span class="text-lg flex-shrink-0">{item.icon}</span>
                {#if !collapsed}
                  <span
                    class="flex-1 text-left"
                    in:fly={{ x: -10, duration: 200 }}
                  >
                    {item.label}
                  </span>
                  {#if item.badge}
                    <span
                      class="flex-shrink-0 px-1.5 py-0.5 text-xs rounded-full bg-primary/10 text-primary"
                    >
                      {item.badge}
                    </span>
                  {/if}
                {/if}
              </button>
            </li>
          {/each}
        </ul>
      </div>
    {/each}
  </nav>

  <!-- Footer -->
  <div class="border-t border-border p-3">
    <button
      onclick={onToggleCollapse}
      class="w-full flex items-center justify-center gap-2 px-2 py-2 rounded-lg text-sm text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
    >
      <svg
        class="w-4 h-4 transition-transform duration-300"
        class:rotate-180={collapsed}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
      >
        <polyline points="15 18 9 12 15 6" />
      </svg>
      {#if !collapsed}
        <span in:fly={{ x: -10, duration: 200 }}>Collapse</span>
      {/if}
    </button>
  </div>
</aside>
