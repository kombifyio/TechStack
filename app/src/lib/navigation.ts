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
  Server,
  Users,
} from "@lucide/svelte";

export interface NavItem {
  id: string;
  href: string;
  labelKey: string; // i18n key
  labelFallback: string; // Fallback if i18n not available
  icon: Component;
  feature?: string; // Feature flag name (optional)
  alwaysShow?: boolean; // Show even if feature is disabled (grayed out)
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
 * Monitoring is one long page, so its children are in-page anchors.
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
    id: "services-servers",
    href: "/services?tab=servers",
    labelKey: "nav.servers",
    labelFallback: "Servers",
    icon: Boxes,
  },
];
const monitoringChildren: NavItem[] = [
  {
    id: "monitoring-servers",
    href: "/monitoring#servers",
    labelKey: "nav.monitoring.servers",
    labelFallback: "Servers",
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
    labelFallback: "Actors",
    icon: Users,
  },
  {
    id: "nodes",
    href: "/nodes",
    labelKey: "nav.nodes",
    labelFallback: "Nodes",
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
 * Grouped navigation for sidebar layout
 */
export const sidebarNavGroups: NavGroup[] = [
  {
    id: "main",
    items: mainNavItems,
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
    labelFallback: "Actors",
    icon: Users,
  },
  {
    id: "embedded-nodes",
    href: "/nodes",
    labelKey: "nav.nodes",
    labelFallback: "Nodes",
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
];

/**
 * Get flat list of all nav items (for inline tab navigation in SaaS mode)
 * Excludes feature-flagged items that are disabled
 */
export function getFlatNavItems(
  featureFlags: Record<string, boolean> = {},
): NavItem[] {
  return mainNavItems.filter((item) => {
    if (!item.feature) return true;
    if (item.alwaysShow) return true;
    return featureFlags[item.feature] === true;
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
  for (const item of mainNavItems) {
    if (isNavItemActive(item, currentPath)) {
      return item;
    }
  }
  return null;
}
