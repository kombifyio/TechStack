import { describe, expect, it } from "vitest";
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";

const appRoot = process.cwd();
const testsRoot = join(appRoot, "tests");

function collectTypeScriptFiles(dir: string): string[] {
  if (!existsSync(dir)) return [];

  return readdirSync(dir).flatMap((entry) => {
    const path = join(dir, entry);
    const stat = statSync(path);
    if (stat.isDirectory()) return collectTypeScriptFiles(path);
    return path.endsWith(".ts") ? [path] : [];
  });
}

function readTestFiles(): { path: string; text: string }[] {
  return collectTypeScriptFiles(testsRoot).map((path) => ({
    path: relative(appRoot, path).replaceAll("\\", "/"),
    text: readFileSync(path, "utf8"),
  }));
}

describe("E2E test process guardrails", () => {
  it("does not hardcode legacy local admin credentials in Playwright tests", () => {
    const offenders = readTestFiles()
      .filter((file) =>
        /admin@techstack\.local|techstack-admin/.test(file.text),
      )
      .map((file) => file.path);

    expect(offenders).toEqual([]);
  });

  it("does not accept 401/403 as release happy-path outcomes", () => {
    const offenders = readTestFiles()
      .filter((file) => !file.path.includes("negative"))
      .filter((file) =>
        /expect\(\[\s*401\s*,\s*403\s*\]\)|expect\(\[\s*403\s*,\s*401\s*\]\)/.test(
          file.text,
        ),
      )
      .map((file) => file.path);

    expect(offenders).toEqual([]);
  });

  it("requires explicit opt-in for fake localStorage auth helpers", () => {
    const offenders = readTestFiles()
      .filter((file) => !file.path.endsWith("helpers/test-utils.ts"))
      .filter(
        (file) =>
          file.text.includes("mockLoggedInContext(") &&
          !/mockLoggedInContext\([^)]*allowMockAuth:\s*true/.test(file.text),
      )
      .map((file) => file.path);

    expect(offenders).toEqual([]);
  });
});
