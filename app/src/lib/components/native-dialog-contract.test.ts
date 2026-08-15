import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";

function svelteFiles(root: string): string[] {
  return readdirSync(root, { withFileTypes: true }).flatMap((entry) => {
    const path = join(root, entry.name);
    if (entry.isDirectory()) return svelteFiles(path);
    return entry.isFile() && entry.name.endsWith(".svelte") ? [path] : [];
  });
}

describe("in-app dialog contract", () => {
  it("does not use browser-native alert, confirm, or prompt dialogs", () => {
    const sourceRoot = join(process.cwd(), "src");
    const violations = svelteFiles(sourceRoot).flatMap((path) =>
      readFileSync(path, "utf8")
        .split(/\r?\n/)
        .map((line, index) => ({
          path: relative(process.cwd(), path).replaceAll("\\", "/"),
          line: index + 1,
          source: line.trim(),
        }))
        .filter(({ source }) => /\b(?:alert|confirm|prompt)\(/.test(source)),
    );

    expect(violations).toEqual([]);
  });
});
