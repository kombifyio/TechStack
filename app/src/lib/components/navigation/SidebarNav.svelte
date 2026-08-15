<script lang="ts">
  /**
   * SidebarNav - Full sidebar navigation for Self-Hosted mode
   * Uses the shared navigation config from lib/navigation.ts
   */
  import { page } from "$app/stores";
  import { fly, slide } from "svelte/transition";
  import { tr } from "$lib/i18n.svelte";
  import { mainNavItems, isNavItemActive, type NavItem } from "$lib/navigation";
  import { authStore } from "$lib/stores/auth.svelte";
  import ThemeToggle from "$lib/components/ui/ThemeToggle.svelte";
  import { stackIdentity } from "$lib/stores/stackIdentity";
  import { StackIdentityBadge } from "$lib/components/open-core";
  import {
    ChevronLeft,
    ChevronRight,
    ChevronUp,
    Settings,
    HelpCircle,
    LogOut,
    ExternalLink,
    Sun,
  } from "@lucide/svelte";

  interface Props {
    collapsed?: boolean;
    onToggleCollapse?: () => void;
    apiVersion?: string;
    onOpenLocalAdmin?: () => void;
    class?: string;
  }

  let {
    collapsed = false,
    onToggleCollapse,
    apiVersion = "",
    onOpenLocalAdmin,
    class: className = "",
  }: Props = $props();

  let userMenuOpen = $state(false);
  let expandedItems = $state<Set<string>>(new Set(["stacks"]));
  let hoverItem = $state<string | null>(null);

  // Get current path for active state
  const currentPath = $derived($page.url.pathname);
  const userEmail = $derived(authStore.userEmail || "User");
  const userInitial = $derived(userEmail.charAt(0).toUpperCase() || "U");

  // Filter nav items based on feature flags
  const visibleNavItems = $derived(
    mainNavItems.filter((item) => {
      if (!item.feature) return true;
      if (item.alwaysShow) return true;
      return true;
    }),
  );

  function isItemDisabled(_item: NavItem): boolean {
    return false;
  }

  function toggleExpanded(itemId: string) {
    if (expandedItems.has(itemId)) {
      expandedItems.delete(itemId);
    } else {
      expandedItems.add(itemId);
    }
    expandedItems = new Set(expandedItems);
  }

  function getLabel(item: NavItem): string {
    const translated = tr(item.labelKey);
    return translated !== item.labelKey ? translated : item.labelFallback;
  }

  async function handleLogout() {
    userMenuOpen = false;
    await authStore.logout({ manualLogin: true });
  }
</script>

<aside
  class="sidebar flex h-full min-w-0 flex-col bg-sidebar text-sidebar-foreground transition-all duration-300 {collapsed
    ? 'w-18'
    : 'w-full lg:w-64'} {className}"
>
  <!-- Logo -->
  <div class="p-4 border-b border-sidebar-border">
    <a
      href="/"
      class="flex h-11 w-full items-center {collapsed
        ? 'justify-center'
        : 'gap-3'}"
      title="kombify-TechStack"
    >
      <img
        src={collapsed
          ? "/kombify-techstack-k.png"
          : "/kombify-techstack-kombi3d.png"}
        alt="kombify TechStack"
        class={collapsed
          ? "h-10 w-10 shrink-0 rounded-xl object-contain"
          : "h-10 w-auto min-w-0 max-w-[11.5rem] object-contain"}
      />
    </a>
    {#if $stackIdentity && !collapsed}
      <div class="mt-3">
        <StackIdentityBadge identity={$stackIdentity} />
      </div>
    {/if}
  </div>

  <!-- Main Navigation -->
  <nav class="flex-1 py-4 overflow-y-auto">
    <ul class="px-3 space-y-1">
      {#each visibleNavItems as item}
        {@const Icon = item.icon}
        {@const isActive = isNavItemActive(item, currentPath)}
        {@const isDisabled = isItemDisabled(item)}
        {@const hasChildren = item.children && item.children.length > 0}
        {@const isExpanded = expandedItems.has(item.id)}

        <li>
          <a
            href={isDisabled ? "#" : item.href}
            onclick={(e) => {
              if (isDisabled) {
                e.preventDefault();
                return;
              }
              if (hasChildren && !collapsed) {
                e.preventDefault();
                toggleExpanded(item.id);
              }
            }}
            onmouseenter={() => (hoverItem = item.id)}
            onmouseleave={() => (hoverItem = null)}
            class="relative flex items-center gap-3 px-3 py-2.5 rounded-xl text-sm font-medium transition-all duration-200 {isActive
              ? 'bg-primary/10 text-primary'
              : 'text-muted-foreground hover:text-foreground hover:bg-muted'} {isDisabled
              ? 'opacity-50 cursor-not-allowed'
              : ''}"
            title={collapsed
              ? getLabel(item)
              : isDisabled
                ? `${getLabel(item)} is disabled`
                : undefined}
          >
            <!-- Active indicator -->
            {#if isActive && !collapsed}
              <div
                class="absolute left-0 top-1/2 -translate-y-1/2 w-1 h-6 rounded-r-full bg-primary"
                in:fly={{ x: -10, duration: 200 }}
              ></div>
            {/if}

            <Icon
              class="w-5 h-5 shrink-0 transition-transform duration-200 {isActive
                ? 'scale-110'
                : ''}"
            />

            {#if !collapsed}
              <span class="flex-1 text-left" in:fly={{ x: -10, duration: 200 }}>
                {getLabel(item)}
              </span>
              {#if item.badge}
                <span
                  class="shrink-0 min-w-5 h-5 px-1.5 text-xs rounded-full bg-primary text-primary-foreground flex items-center justify-center"
                >
                  {item.badge}
                </span>
              {/if}
              {#if hasChildren}
                <ChevronRight
                  class="w-4 h-4 text-muted-foreground transition-transform {isExpanded
                    ? 'rotate-90'
                    : ''}"
                />
              {/if}
            {/if}

            <!-- Hover tooltip for collapsed state -->
            {#if collapsed && hoverItem === item.id}
              <div
                class="absolute left-full ml-2 px-3 py-1.5 rounded-lg bg-popover border border-border shadow-xl text-foreground text-sm whitespace-nowrap z-50"
                in:fly={{ x: -5, duration: 150 }}
              >
                {getLabel(item)}
              </div>
            {/if}
          </a>

          <!-- Sub-navigation -->
          {#if hasChildren && isExpanded && !collapsed}
            <ul
              class="mt-1 ml-4 pl-4 border-l border-border space-y-0.5"
              in:slide={{ duration: 200 }}
            >
              {#each item.children as child}
                {@const ChildIcon = child.icon}
                {@const isChildActive = currentPath === child.href}
                <li>
                  <a
                    href={child.href}
                    class="flex items-center gap-2 px-3 py-2 rounded-lg text-sm transition-colors {isChildActive
                      ? 'text-primary font-medium'
                      : 'text-muted-foreground hover:text-foreground hover:bg-muted/50'}"
                  >
                    <ChildIcon class="w-4 h-4" />
                    <span>{getLabel(child)}</span>
                  </a>
                </li>
              {/each}
            </ul>
          {/if}
        </li>
      {/each}
    </ul>
  </nav>

  <!-- User Section -->
  <div class="p-3 border-t border-sidebar-border">
    {#if !collapsed}
      <div class="user-menu-container relative">
        <button
          onclick={() => (userMenuOpen = !userMenuOpen)}
          class="w-full flex items-center gap-3 p-2 rounded-xl bg-sidebar-accent/30 hover:bg-sidebar-accent transition-colors text-left"
        >
          <div
            class="w-9 h-9 rounded-full bg-linear-to-br from-primary to-primary/60 flex items-center justify-center text-primary-foreground font-semibold text-sm shrink-0"
          >
            {userInitial}
          </div>
          <div class="flex-1 min-w-0">
            <p class="text-sm font-medium text-foreground truncate">
              {userEmail}
            </p>
            <p class="text-xs text-muted-foreground">Administrator</p>
          </div>
          <ChevronUp
            class="w-4 h-4 text-muted-foreground transition-transform {userMenuOpen
              ? ''
              : 'rotate-180'}"
          />
        </button>

        <!-- User Menu -->
        {#if userMenuOpen}
          <div
            class="absolute bottom-full left-0 right-0 mb-2 py-2 bg-popover rounded-lg border border-border shadow-lg z-50"
            in:fly={{ y: 10, duration: 150 }}
          >
            <a
              href="/settings"
              class="flex items-center gap-3 px-4 py-2 text-sm text-muted-foreground hover:bg-accent hover:text-foreground transition-colors"
              onclick={() => (userMenuOpen = false)}
            >
              <Settings class="w-4 h-4" />
              {tr("nav.settings")}
            </a>
            <a
              href="https://docs.kombify.io/techstack"
              target="_blank"
              rel="noopener"
              class="flex items-center gap-3 px-4 py-2 text-sm text-muted-foreground hover:bg-accent hover:text-foreground transition-colors"
              onclick={() => (userMenuOpen = false)}
            >
              <HelpCircle class="w-4 h-4" />
              {tr("nav.help")}
            </a>
            <div
              class="flex items-center gap-3 px-4 py-2 text-sm text-muted-foreground"
            >
              <Sun class="w-4 h-4" />
              <span class="flex-1">Theme</span>
              <ThemeToggle />
            </div>
            {#if onOpenLocalAdmin}
              <button
                onclick={() => {
                  userMenuOpen = false;
                  onOpenLocalAdmin?.();
                }}
                class="w-full flex items-center gap-3 px-4 py-2 text-sm text-muted-foreground hover:bg-accent hover:text-foreground transition-colors text-left"
              >
                <ExternalLink class="w-4 h-4" />
                Open Local Admin
              </button>
            {/if}
            <div class="border-t border-border my-2"></div>
            {#if apiVersion}
              <div class="px-4 py-2 text-xs text-muted-foreground">
                Version {apiVersion}
              </div>
            {/if}
            <button
              onclick={handleLogout}
              class="w-full flex items-center gap-3 px-4 py-2 text-sm text-destructive hover:bg-destructive/10 transition-colors text-left"
            >
              <LogOut class="w-4 h-4" />
              Logout
            </button>
          </div>
        {/if}
      </div>
    {:else}
      <button
        class="w-full flex items-center justify-center p-2 rounded-lg hover:bg-sidebar-accent transition-colors"
        onclick={() => (userMenuOpen = !userMenuOpen)}
      >
        <div
          class="w-8 h-8 rounded-full bg-linear-to-br from-primary to-primary/60 flex items-center justify-center text-primary-foreground font-semibold text-sm"
        >
          {userInitial}
        </div>
      </button>
    {/if}

    <!-- Collapse Button -->
    {#if onToggleCollapse}
      <button
        onclick={onToggleCollapse}
        class="w-full flex items-center justify-center gap-2 px-2 py-2 mt-2 rounded-lg text-sm text-muted-foreground hover:text-foreground hover:bg-sidebar-accent transition-colors"
      >
        {#if collapsed}
          <ChevronRight class="w-4 h-4" />
        {:else}
          <ChevronLeft class="w-4 h-4" />
          <span in:fly={{ x: -10, duration: 200 }}>Collapse</span>
        {/if}
      </button>
    {/if}
  </div>
</aside>
