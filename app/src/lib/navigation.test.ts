import { describe, expect, it } from "vitest";
import {
  CREATE_HOMELAB_HREF,
  SERVERS_HREF,
  SETTINGS_HREF,
  embeddedNavItems,
  homelabNavItems,
  mainNavItems,
  sidebarNavGroups,
  visibleNavItems,
  type NavItem,
} from "./navigation";

const item = (id: string, extra: Partial<NavItem> = {}): NavItem => ({
  id,
  href: `/${id}`,
  labelKey: `nav.${id}`,
  labelFallback: id,
  icon: (() => null) as unknown as NavItem["icon"],
  ...extra,
});

describe("visibleNavItems", () => {
  it("keeps ungated entries and hides a gated entry while its flag is off", () => {
    const items = [item("a"), item("b", { feature: "beta" })];
    expect(visibleNavItems(items).map((entry) => entry.id)).toEqual(["a", "b"]);
    expect(
      visibleNavItems(items, () => false).map((entry) => entry.id),
    ).toEqual(["a"]);
    expect(
      visibleNavItems(items, (feature) => feature === "beta").map(
        (entry) => entry.id,
      ),
    ).toEqual(["a", "b"]);
  });

  it("lists an alwaysShow entry whatever its flag says", () => {
    const items = [item("b", { feature: "beta", alwaysShow: true })];
    expect(visibleNavItems(items, () => false)).toHaveLength(1);
  });
});

describe("navigation structure", () => {
  const everyItem = [
    ...sidebarNavGroups.flatMap((group) => group.items),
    ...embeddedNavItems,
  ];

  it("has exactly one place for the servers of the homelab", () => {
    const places = (items: readonly NavItem[]) =>
      items.filter((entry) => entry.href === SERVERS_HREF);
    expect(places(mainNavItems)).toHaveLength(1);
    expect(places(embeddedNavItems)).toHaveLength(1);
    // No section flyout repeats the server list under another name.
    for (const entry of everyItem) {
      for (const child of entry.children ?? []) {
        expect(child.href).not.toBe(SERVERS_HREF);
        expect(child.labelKey).not.toBe("nav.servers");
      }
    }
  });

  it("offers creating a homelab and the settings in both shells", () => {
    expect(homelabNavItems.map((entry) => entry.href)).toEqual([
      CREATE_HOMELAB_HREF,
      SETTINGS_HREF,
    ]);
    const embedded = embeddedNavItems.map((entry) => entry.href);
    expect(embedded).toContain(CREATE_HOMELAB_HREF);
    expect(embedded).toContain(SETTINGS_HREF);
  });

  it("uses the glossary names, never Actors or Nodes", () => {
    const labels = everyItem.map((entry) => entry.labelFallback);
    expect(labels).toContain("People & agents");
    expect(labels).toContain("Servers");
    expect(labels.join(" ")).not.toMatch(/\b(Actors|Nodes?)\b/);
  });

  it("has no duplicate entry ids and every monitoring child is an anchor on /monitoring", () => {
    const ids = sidebarNavGroups.flatMap((group) =>
      group.items.map((entry) => entry.id),
    );
    expect(new Set(ids).size).toBe(ids.length);
    const monitoring = mainNavItems.find((entry) => entry.id === "monitoring");
    expect(monitoring?.children?.map((child) => child.href)).toEqual([
      "/monitoring#now",
      "/monitoring#alerts",
      "/monitoring#history",
    ]);
  });
});
