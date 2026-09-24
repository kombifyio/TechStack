/**
 * Product nav config → package NavItems, for both shells.
 *
 * The sidebar and the embedded tab bar used to map separately and each
 * dropped `children`, which is why no flyout ever opened in Techstack. One
 * mapper now carries children (labels localized here), the live preview for
 * the section, and the coach anchor.
 */
import type {
  NavItem as PackageNavItem,
  NavPreview,
} from "@kombiverselabs/ui/shell";
import type { NavItem as ProductNavItem } from "#lib/navigation.js";
import { tr } from "#lib/i18n.svelte.js";
import { navPreview } from "#lib/stores/navPreview.svelte.js";

export function navLabel(
  item: Pick<ProductNavItem, "labelKey" | "labelFallback">,
): string {
  const translated = tr(item.labelKey);
  return translated !== item.labelKey ? translated : item.labelFallback;
}

function text(key: string, fallback: string): string {
  const translated = tr(key);
  return translated !== key ? translated : fallback;
}

/** Section key shared by the sidebar id and its `embedded-` twin. */
function sectionOf(item: ProductNavItem): string {
  return item.id.replace(/^embedded-/, "");
}

/**
 * Live entries per section, from data the layout already loaded. A section
 * still loading shows its empty label rather than a spinner: a flyout is a
 * glance, not a page.
 */
function previewFor(section: string): NavPreview | undefined {
  switch (section) {
    case "services": {
      const services = navPreview.services.data ?? [];
      return {
        caption: text("nav.preview.services", "Services"),
        items: services.map((service) => ({
          label: service.name,
          href: `/services?service=${encodeURIComponent(service.id)}`,
          meta: service.health.state,
        })),
        emptyLabel: text(
          "nav.preview.noServices",
          "No services registered yet.",
        ),
      };
    }
    case "monitoring": {
      const active = navPreview.activeAlertCount;
      return {
        caption: text("nav.preview.monitoring", "Monitoring"),
        items: [
          {
            label:
              active > 0
                ? `${active} ${text("nav.preview.activeAlerts", "active alerts")}`
                : text("nav.preview.noAlerts", "No active alerts."),
          },
        ],
      };
    }
    default:
      return undefined;
  }
}

export function toPackageNavItems(
  items: readonly ProductNavItem[],
): PackageNavItem[] {
  return items.map((item) => ({
    href: item.href,
    label: navLabel(item),
    icon: item.icon,
    badge: item.badge ?? undefined,
    disabled: false,
    external: item.external,
    anchor: item.anchor,
    children: item.children?.map((child) => ({
      href: child.href,
      label: navLabel(child),
      icon: child.icon,
    })),
    preview: previewFor(sectionOf(item)),
  }));
}
