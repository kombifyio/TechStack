/**
 * Navigation Configuration - Single Source of Truth
 *
 * This file defines ALL navigation items for kombify-TechStack.
 * Both Self-Hosted (sidebar) and SaaS (inline tabs) layouts use this config.
 *
 * Architecture:
 * - Self-Hosted Mode: Full left sidebar with all items
 * - SaaS Embedded Mode: Inline tabs in header (no sidebar)
 */

import type { Component } from "svelte";
import {
  Activity,
  Wallet,
  Boxes,
  Home,
  LayoutGrid,
  Plus,
  Server,
  Settings,
  Users,
} from "@lucide/svelte";

export interface NavItem {
  id: string;
  href: string;
  labelKey: string; // i18n key
  labelFallback: string; // Fallback if i18n not available
  icon: Component;
  feature?: string; // Feature flag name (optional): hidden while the flag is off
  alwaysShow?: boolean; // Show even if the feature is off (see visibleNavItems)
  badge?: number | null; // Optional badge count
  children?: NavItem[]; // Sub-navigation items
  external?: boolean; // External URL — render with target="_blank" rel="noopener"
  /** Coach-mark anchor (data-onboarding-anchor), ONBOARDING-JOURNEY-STANDARD §6 */
  anchor?: string;
}

export interface NavGroup {
  id: string;
  titleKey?: string;
  titleFallback?: string;
  items: NavItem[];
}

/**
 * Sub-navigation per section. The flyout on a section's nav item lists these
 * (and the section's live entries, see nav-items.ts); both shells share them.
 * Monitoring is one long page, so its children are in-page anchors, and every
 * anchor has a section on the page.
 *
 * Servers have ONE place, the Servers entry (`SERVERS_HREF`): neither the
 * Services nor the Monitoring flyout lists a second "Servers" entry.
 */
const servicesChildren: NavItem[] = [
  {
    id: "services-all",
    href: "/services",
    labelKey: "nav.allServices",
    labelFallback: "All services",
    icon: Boxes,
  },
  {
    id: "services-placement",
    href: "/services?tab=servers",
    labelKey: "nav.servicesPlacement",
    labelFallback: "Placement",
    icon: Boxes,
  },
];
const monitoringChildren: NavItem[] = [
  {
    id: "monitoring-now",
    href: "/monitoring#now",
    labelKey: "nav.monitoring.now",
    labelFallback: "Right now",
    icon: Activity,
  },
  {
    id: "monitoring-alerts",
    href: "/monitoring#alerts",
    labelKey: "nav.monitoring.alerts",
    labelFallback: "Alerts",
    icon: Activity,
  },
  {
    id: "monitoring-history",
    href: "/monitoring#history",
    labelKey: "nav.monitoring.history",
    labelFallback: "History",
    icon: Activity,
  },
];

/**
 * The product start page. `/` only redirects here once signed in, and the
 * brand in the rail links here.
 */
export const START_PAGE_HREF = "/dashboard";

/** The one list of the homelab's servers. */
export const SERVERS_HREF = "/servers";

/** Where a new homelab is created (the creation wizard). */
export const CREATE_HOMELAB_HREF = "/stacks/new";

/** Product settings; the Homelab tool tab lives under it. */
export const SETTINGS_HREF = "/settings";

/** The route is the start page: the brand mark cycles and unfolds the rail. */
export function isStartPage(pathname: string): boolean {
  return pathname.replace(/\/+$/, "") === START_PAGE_HREF;
}

/** Id of the content pane beside the rail; the resize separator controls it. */
export const CONTENT_PANE_ID = "app-content";

/**
 * Main navigation items - used in both layouts
 */
export const mainNavItems: NavItem[] = [
  {
    id: "dashboard",
    href: START_PAGE_HREF,
    labelKey: "nav.dashboard",
    labelFallback: "Dashboard",
    icon: Home,
  },
  {
    id: "apps",
    href: "/apps",
    labelKey: "nav.apps",
    labelFallback: "Apps",
    icon: LayoutGrid,
  },
  {
    id: "actors",
    href: "/actors",
    labelKey: "nav.actors",
    labelFallback: "People & agents",
    icon: Users,
  },
  {
    id: "servers",
    href: SERVERS_HREF,
    labelKey: "nav.servers",
    labelFallback: "Servers",
    icon: Server,
  },
  {
    id: "services",
    href: "/services",
    labelKey: "nav.services",
    labelFallback: "Services",
    icon: Boxes,
    children: servicesChildren,
    anchor: "techstack-services-nav",
  },
  {
    id: "monitoring",
    href: "/monitoring",
    labelKey: "nav.monitoring",
    labelFallback: "Monitoring",
    icon: Activity,
    children: monitoringChildren,
  },
  // Simulation removed in cleanup plan 2026-04-27 phase 3.1.
  // Future simulation tooling lives in the kombify-Sim repository.
  {
    id: "wallet",
    href: "/wallet",
    labelKey: "nav.wallet",
    labelFallback: "Wallet",
    icon: Wallet,
  },
];

/**
 * The homelab entries below the main list: creating a homelab and the
 * settings (they used to live only in the account menu and the dashboard).
 */
export const homelabNavItems: NavItem[] = [
  {
    id: "create-homelab",
    href: CREATE_HOMELAB_HREF,
    labelKey: "nav.createHomelab",
    labelFallback: "Create homelab",
    icon: Plus,
  },
  {
    id: "settings",
    href: SETTINGS_HREF,
    labelKey: "nav.settings",
    labelFallback: "Settings",
    icon: Settings,
  },
];

/**
 * Grouped navigation for sidebar layout
 */
export const sidebarNavGroups: NavGroup[] = [
  {
    id: "main",
    items: mainNavItems,
  },
  {
    id: "homelab",
    titleKey: "nav.group.homelab",
    titleFallback: "Homelab",
    items: homelabNavItems,
  },
];

/**
 * Curated navigation items for SaaS embedded mode (inline tabs).
 * Keeps the embedded UI focused on the most relevant views.
 */
export const embeddedNavItems: NavItem[] = [
  {
    id: "embedded-dashboard",
    href: START_PAGE_HREF,
    labelKey: "nav.dashboard",
    labelFallback: "Dashboard",
    icon: Home,
  },
  {
    id: "embedded-apps",
    href: "/apps",
    labelKey: "nav.apps",
    labelFallback: "Apps",
    icon: LayoutGrid,
  },
  {
    id: "embedded-actors",
    href: "/actors",
    labelKey: "nav.actors",
    labelFallback: "People & agents",
    icon: Users,
  },
  {
    id: "embedded-servers",
    href: SERVERS_HREF,
    labelKey: "nav.servers",
    labelFallback: "Servers",
    icon: Server,
  },
  {
    id: "embedded-services",
    href: "/services",
    labelKey: "nav.services",
    labelFallback: "Services",
    icon: Boxes,
    children: servicesChildren,
    anchor: "techstack-services-nav",
  },
  {
    id: "embedded-monitoring",
    href: "/monitoring",
    labelKey: "nav.monitoring",
    labelFallback: "Monitoring",
    icon: Activity,
    children: monitoringChildren,
  },
  {
    id: "embedded-wallet",
    href: "/wallet",
    labelKey: "nav.wallet",
    labelFallback: "Wallet",
    icon: Wallet,
  },
  // Cloud owns account navigation around the embed, but the tool's own
  // settings (the Homelab tab) and creation are reachable only from here.
  {
    id: "embedded-create-homelab",
    href: CREATE_HOMELAB_HREF,
    labelKey: "nav.createHomelab",
    labelFallback: "Create homelab",
    icon: Plus,
  },
  {
    id: "embedded-settings",
    href: SETTINGS_HREF,
    labelKey: "nav.settings",
    labelFallback: "Settings",
    icon: Settings,
  },
];

/**
 * The entries a shell shows. An item that names a `feature` is hidden while
 * that flag is off; `alwaysShow` keeps it listed regardless. Both shells
 * filter through this one function, so a gated entry can never appear in one
 * rail and not in the other.
 */
export function visibleNavItems(
  items: readonly NavItem[],
  isFeatureEnabled: (feature: string) => boolean = () => true,
): NavItem[] {
  return items.filter((item) => {
    if (!item.feature) return true;
    if (item.alwaysShow) return true;
    return isFeatureEnabled(item.feature);
  });
}

/**
 * Check if a nav item is active based on current path
 */
export function isNavItemActive(item: NavItem, currentPath: string): boolean {
  if (currentPath === item.href) return true;
  if (item.children) {
    return item.children.some((child) => currentPath === child.href);
  }
  return currentPath.startsWith(item.href + "/");
}

/**
 * Get the parent nav item for a given path
 */
export function getParentNavItem(currentPath: string): NavItem | null {
  for (const item of [...mainNavItems, ...homelabNavItems]) {
    if (isNavItemActive(item, currentPath)) {
      return item;
    }
  }
  return null;
}
