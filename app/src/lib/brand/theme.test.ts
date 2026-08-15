import { describe, expect, it } from "vitest";
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

const appHtml = readFileSync(resolve(process.cwd(), "src/app.html"), "utf-8");
const appCss = readFileSync(resolve(process.cwd(), "src/app.css"), "utf-8");
const loginPage = readFileSync(
  resolve(process.cwd(), "src/routes/login/+page.svelte"),
  "utf-8",
);
const registerPage = readFileSync(
  resolve(process.cwd(), "src/routes/register/+page.svelte"),
  "utf-8",
);
const footerModern = readFileSync(
  resolve(process.cwd(), "src/lib/components/FooterModern.svelte"),
  "utf-8",
);
const sidebarNav = readFileSync(
  resolve(process.cwd(), "src/lib/components/navigation/SidebarNav.svelte"),
  "utf-8",
);
const inlineTabNav = readFileSync(
  resolve(process.cwd(), "src/lib/components/navigation/InlineTabNav.svelte"),
  "utf-8",
);
const openCoreIdentity = readFileSync(
  resolve(process.cwd(), "src/lib/components/open-core/identity.ts"),
  "utf-8",
);
const sourceRoot = resolve(process.cwd(), "src");
const maxConcurrentSourceReads = 8;

function sourceFiles(path: string): string[] {
  return readdirSync(path, { withFileTypes: true }).flatMap((entry) => {
    const fullPath = resolve(path, entry.name);
    if (entry.isDirectory()) return sourceFiles(fullPath);
    if (!/\.(css|html|svelte|ts)$/.test(entry.name)) return [];
    return [fullPath];
  });
}

async function filesContaining(
  files: string[],
  needle: string,
  readText: (file: string) => Promise<string> = (file) =>
    readFile(file, "utf-8"),
): Promise<string[]> {
  const matches: string[] = [];
  for (
    let offset = 0;
    offset < files.length;
    offset += maxConcurrentSourceReads
  ) {
    const batch = files.slice(offset, offset + maxConcurrentSourceReads);
    const batchMatches = await Promise.all(
      batch.map(async (file) =>
        (await readText(file)).includes(needle) ? file : null,
      ),
    );
    matches.push(
      ...batchMatches.filter((file): file is string => file !== null),
    );
  }
  return matches;
}

describe("TechStack brand theme", () => {
  it("uses the TechStack theme class, not the Cloud theme", () => {
    expect(appHtml).toContain('class="theme-techstack"');
    expect(appHtml).not.toContain("theme-cloud");
  });

  it("uses the TechStack wordmark on the login entry", () => {
    expect(
      existsSync(
        resolve(process.cwd(), "static/kombify-techstack-kombi3d.png"),
      ),
    ).toBe(true);
    expect(loginPage).toContain('src="/kombify-techstack-kombi3d.png"');
    expect(registerPage).toContain('src="/kombify-techstack-kombi3d.png"');
    expect(footerModern).toContain('return "/kombify-techstack-kombi3d.png"');
    expect(loginPage).toContain('alt="kombify TechStack"');
  });

  it("uses the TechStack logo assets in navigation chrome", () => {
    expect(
      existsSync(resolve(process.cwd(), "static/kombify-techstack-k.png")),
    ).toBe(true);
    expect(
      existsSync(
        resolve(process.cwd(), "static/kombify-techstack-kombi3d.png"),
      ),
    ).toBe(true);
    expect(
      existsSync(resolve(process.cwd(), "static/kombify-techstack.png")),
    ).toBe(false);
    expect(sidebarNav).toContain('"/kombify-techstack-k.png"');
    expect(sidebarNav).toContain('"/kombify-techstack-kombi3d.png"');
    expect(inlineTabNav).toContain('src="/kombify-techstack-kombi3d.png"');
    expect(sidebarNav).not.toContain('"/kombify-techstack.png"');
    expect(inlineTabNav).not.toContain('src="/kombify-techstack.png"');
    expect(footerModern).toContain('src="/kombify-techstack-k.png"');
    expect(sidebarNav).not.toContain('src="/kombify-icon.png"');
    expect(inlineTabNav).not.toContain('src="/kombify-icon.png"');
    expect(footerModern).not.toContain('src="/kombify-techstack-icon.png"');
  });

  it("keeps TechStack accent tokens aligned to the Brand navy values", () => {
    expect(appCss).toContain(".theme-techstack");
    expect(appCss).toMatch(/oklch\(0\.3(?:0)? 0\.09 255\)/);
    expect(appCss).toContain("oklch(0.55 0.14 255)");
    expect(appCss).not.toContain("oklch(0.65 0.18 200)");
    expect(appCss).not.toContain("oklch(0.78 0.16 200)");
    expect(appCss).not.toContain("oklch(0.62 0.18 155)");
    expect(appCss).not.toContain("oklch(0.72 0.16 155)");
  });

  it("keeps the identity UI in the repository-local open-core boundary", () => {
    const privateUiPackage = "@kom" + "biverse" + "labs/ui";
    expect(openCoreIdentity).toContain("normalizeStackIdentity");
    expect(appCss).not.toContain(privateUiPackage);
  });

  it("does not import the private UI package from exported app source", async () => {
    const privateUiPackage = "@kom" + "biverse" + "labs/ui";
    const offenders = await filesContaining(
      sourceFiles(sourceRoot),
      privateUiPackage,
    );
    expect(offenders).toEqual([]);
  });

  it("scans source files concurrently without losing offender paths", async () => {
    let inFlight = 0;
    let maxInFlight = 0;
    const retiredAccent = "cyan" + "-";
    const candidates = Array.from(
      { length: 12 },
      (_, index) => `source-${index}.svelte`,
    );
    const matches = await filesContaining(
      candidates,
      retiredAccent,
      async (file) => {
        const index = candidates.indexOf(file);
        inFlight += 1;
        maxInFlight = Math.max(maxInFlight, inFlight);
        await new Promise((resolveRead) =>
          setTimeout(resolveRead, 1 + ((12 - index) % 3)),
        );
        inFlight -= 1;
        return index === 1 || index === 3
          ? `bg-${retiredAccent}500`
          : "bg-navy-500";
      },
    );

    expect(maxInFlight).toBeGreaterThan(1);
    expect(maxInFlight).toBeLessThanOrEqual(maxConcurrentSourceReads);
    expect(matches).toEqual(["source-1.svelte", "source-3.svelte"]);
  });

  it("fails closed when one source file cannot be read", async () => {
    const retiredAccent = "cyan" + "-";
    await expect(
      filesContaining(["unreadable.svelte"], retiredAccent, async () => {
        throw new Error("source read failed");
      }),
    ).rejects.toThrow("source read failed");
  });

  it("does not keep the retired cyan accent in source UI classes", async () => {
    const retiredAccent = "cyan" + "-";
    const offenders = (
      await filesContaining(sourceFiles(sourceRoot), retiredAccent)
    ).map((file) => file.replace(process.cwd(), ""));

    expect(offenders).toEqual([]);
  });
});
