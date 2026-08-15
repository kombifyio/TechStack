import { existsSync, readFileSync, readdirSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

function source(path: string): string {
  return readFileSync(resolve(process.cwd(), path), "utf8");
}

describe("canonical inventory card standard", () => {
  it("composes the shared service cards instead of drawing a second card", () => {
    const serviceList = source(
      "src/lib/components/inventory/ServiceList.svelte",
    );

    expect(serviceList).toContain("ServiceCard,");
    expect(serviceList).toContain("ServiceCardCompact,");
    expect(serviceList).toContain('from "$lib/components/open-core"');
    expect(serviceList).toContain("<ServiceCard");
    expect(serviceList).toContain("<ServiceCardCompact");
    expect(serviceList).not.toContain("StatusBadge");
    expect(serviceList).not.toContain("<details");
    expect(
      existsSync(
        resolve(
          process.cwd(),
          "src/lib/components/inventory/StatusBadge.svelte",
        ),
      ),
    ).toBe(false);
    expect(
      existsSync(
        resolve(
          process.cwd(),
          "src/lib/components/open-core/ServiceCard.svelte",
        ),
      ),
    ).toBe(true);
    expect(
      existsSync(
        resolve(
          process.cwd(),
          "src/lib/components/open-core/ServiceCardCompact.svelte",
        ),
      ),
    ).toBe(true);
  });

  it("routes every direct service-card render through the Brand authority", () => {
    const srcRoot = resolve(process.cwd(), "src");
    const componentFiles = readdirSync(srcRoot, {
      recursive: true,
      withFileTypes: true,
    })
      .filter((entry) => entry.isFile() && entry.name.endsWith(".svelte"))
      .map((entry) => resolve(entry.parentPath, entry.name));

    const directRenderers = componentFiles.filter((path) =>
      /<ServiceCard(?:Compact)?\b/.test(readFileSync(path, "utf8")),
    );

    expect(directRenderers.length).toBeGreaterThan(0);
    for (const path of directRenderers) {
      expect(readFileSync(path, "utf8")).toContain(
        'from "$lib/components/open-core"',
      );
    }
  });

  it("keeps server facts inside the Brand-owned ServerCard standard", () => {
    const serverList = source("src/lib/components/inventory/ServerList.svelte");

    expect(serverList).toContain("<ServerCard");
    expect(serverList).toContain('from "$lib/components/open-core"');
    expect(serverList).toContain("facts={item.facts}");
    expect(serverList).not.toContain("StatusBadge");
    expect(
      existsSync(
        resolve(process.cwd(), "src/lib/components/open-core/ServerCard.svelte"),
      ),
    ).toBe(true);
    expect(
      existsSync(resolve(process.cwd(), "src/lib/components/open-core/server.ts")),
    ).toBe(true);

    const srcRoot = resolve(process.cwd(), "src");
    const directRenderers = readdirSync(srcRoot, {
      recursive: true,
      withFileTypes: true,
    })
      .filter((entry) => entry.isFile() && entry.name.endsWith(".svelte"))
      .map((entry) => resolve(entry.parentPath, entry.name))
      .filter((path) => /<ServerCard\b/.test(readFileSync(path, "utf8")));

    expect(directRenderers.length).toBeGreaterThan(0);
    for (const path of directRenderers) {
      expect(readFileSync(path, "utf8")).toContain(
        'from "$lib/components/open-core"',
      );
    }
  });
});
