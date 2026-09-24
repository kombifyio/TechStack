<script lang="ts">
  /**
   * Product sidebar: package SidebarNav chrome plus Techstack header and the
   * shared UserMenu in the foot.
   */
  import { page } from "$app/state";
  import { mainNavItems } from "#lib/navigation.js";
  import TechstackBrandLogo from "#lib/components/TechstackBrandLogo.svelte";
  import { toPackageNavItems } from "./nav-items.js";
  import UserMenu from "./UserMenu.svelte";
  import { stackIdentity } from "#lib/stores/stackIdentity.js";
  import GettingStartedPanel from "#lib/components/onboarding/GettingStartedPanel.svelte";
  import {
    NotificationBell,
    StackIdentityBadge,
  } from "#lib/components/open-core/index.js";
  import { tr } from "#lib/i18n.svelte.js";
  import {
    SidebarNav as PackageSidebarNav,
    type NavItem,
  } from "@kombiverselabs/ui/shell";

  interface Props {
    collapsed?: boolean;
    onToggleCollapse?: () => void;
    /**
     * Rail geometry. This wrapper is a pass-through: the layout owns the
     * width and its persistence, the package owns the drag interaction.
     * Omitting these from Props once meant the layout's values were silently
     * dropped here and the rail could not be resized at all.
     */
    width?: number;
    onResize?: (width: number) => void;
    onNavigate?: () => void;
    minWidth?: number;
    maxWidth?: number;
    apiVersion?: string;
    onOpenLocalAdmin?: () => void;
    class?: string;
  }

  let {
    collapsed = false,
    onToggleCollapse,
    width,
    onResize,
    onNavigate,
    minWidth,
    maxWidth,
    apiVersion = "",
    onOpenLocalAdmin,
    class: className = "",
  }: Props = $props();

  const currentPath = $derived(page.url.pathname);

  const visibleNavItems = $derived(
    mainNavItems.filter((item) => {
      if (!item.feature) return true;
      if (item.alwaysShow) return true;
      return true;
    }),
  );

  const packageItems = $derived<NavItem[]>(toPackageNavItems(visibleNavItems));
</script>

<PackageSidebarNav
  items={packageItems}
  {currentPath}
  {collapsed}
  {onToggleCollapse}
  {width}
  {onResize}
  {onNavigate}
  {minWidth}
  {maxWidth}
  class={className}
>
  {#snippet header()}
    <a
      href="/"
      class="flex h-11 w-full items-center {collapsed
        ? 'justify-center'
        : 'gap-3'}"
      title="kombify-Techstack"
    >
      {#if collapsed}
        <TechstackBrandLogo
          collapsed
          class="h-10 w-10 shrink-0 object-contain"
        />
      {:else}
        <TechstackBrandLogo
          sizes="214px"
          class="h-10 w-auto max-w-full object-contain object-left"
        />
      {/if}
    </a>
    {#if $stackIdentity && !collapsed}
      <div class="mt-3">
        <StackIdentityBadge identity={$stackIdentity} />
      </div>
    {/if}
  {/snippet}

  {#snippet onboarding(railCollapsed: boolean)}
    <!-- Phase two, and its only surface: progress lives where the user
         already looks. It is deliberately NOT also in the page content. -->
    <GettingStartedPanel collapsed={railCollapsed} />
  {/snippet}

  {#snippet notifications(railCollapsed: boolean)}
    <NotificationBell
      apiBase="/api/v1/notifications"
      variant="rail"
      label={railCollapsed ? undefined : tr("nav.notifications")}
      centerHref="/settings"
      settingsHref="/settings"
    />
  {/snippet}

  {#snippet footer()}
    <UserMenu
      placement={collapsed ? "right" : "up"}
      compact={collapsed}
      showLogout
      {apiVersion}
      {onOpenLocalAdmin}
    />
  {/snippet}
</PackageSidebarNav>
