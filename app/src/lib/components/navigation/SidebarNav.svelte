<script lang="ts">
  /**
   * Product sidebar: package SidebarNav chrome plus Techstack header and the
   * shared UserMenu in the foot.
   */
  import { page } from "$app/state";
  import {
    CONTENT_PANE_ID,
    START_PAGE_HREF,
    isStartPage,
    mainNavItems,
  } from "#lib/navigation.js";
  import { toPackageNavItems } from "./nav-items.js";
  import UserMenu from "./UserMenu.svelte";
  import { stackIdentity } from "#lib/stores/stackIdentity.js";
  import TechstackBrandLogo from "#lib/components/TechstackBrandLogo.svelte";
  import GettingStartedPanel from "#lib/components/onboarding/GettingStartedPanel.svelte";
  import {
    NotificationBell,
    StackIdentityBadge,
  } from "#lib/components/open-core/index.js";
  import { getLocale, tr } from "#lib/i18n.svelte.js";
  import { ownerChannelOptions } from "#lib/notifications/owner-channel.js";
  import {
    SidebarNav as PackageSidebarNav,
    type NavItem,
    type RailHeaderContext,
  } from "@kombiverselabs/ui/shell";

  interface Props {
    collapsed?: boolean;
    /**
     * Told the target fold state: a drag past the minimum folds, a drag out of
     * a folded rail, the brand on the start page and the toggle unfold.
     */
    onCollapsedChange?: (collapsed: boolean) => void;
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
    onCollapsedChange,
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
  const onStartPage = $derived(isStartPage(currentPath));
  const visibleNavItems = $derived(
    mainNavItems.filter((item) => {
      if (!item.feature) return true;
      if (item.alwaysShow) return true;
      return true;
    }),
  );

  const packageItems = $derived<NavItem[]>(toPackageNavItems(visibleNavItems));

  /**
   * The rail head shows the elongated Techstack lockup (owner, 2026-10-04: the
   * tools keep their product logo; the interactive BrandMark is kombify Cloud
   * only for now). It keeps the rail standard's start-page rule: a pointer
   * click on the start page unfolds a folded rail instead of navigating; a
   * keyboard activation only does so for a folded rail and is otherwise the
   * plain link.
   */
  function expandFromBrand(event: MouseEvent, rail: RailHeaderContext) {
    if (!rail.isStartPage) return;
    if (event.defaultPrevented || event.button !== 0) return;
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey)
      return;
    if (event.detail === 0 && !rail.collapsed) return;
    event.preventDefault();
    rail.expand();
  }
</script>

<PackageSidebarNav
  items={packageItems}
  {currentPath}
  {collapsed}
  {onCollapsedChange}
  {width}
  {onResize}
  {onNavigate}
  {minWidth}
  {maxWidth}
  isStartPage={onStartPage}
  resizeControls={CONTENT_PANE_ID}
  collapseLabel={tr("ui.layout.collapseSidebar")}
  expandLabel={tr("ui.layout.expandSidebar")}
  resizeLabel={tr("ui.layout.resizeSidebar")}
  collapsedValueLabel={tr("ui.layout.sidebarCollapsed")}
  class={className}
>
  {#snippet header(rail)}
    <a
      href={START_PAGE_HREF}
      class="flex h-11 w-full items-center {rail.collapsed
        ? 'justify-center'
        : 'gap-3'}"
      onclick={(event) => expandFromBrand(event, rail)}
    >
      {#if rail.collapsed}
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
    {#if $stackIdentity && !rail.collapsed}
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
      centerHref="/notifications"
      settingsHref="/settings#notification-preferences"
      locale={getLocale()}
      {...ownerChannelOptions()}
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
