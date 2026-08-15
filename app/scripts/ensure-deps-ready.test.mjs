import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";

import {
  dependenciesReady,
  frozenManifestReady,
} from "./ensure-deps-ready.mjs";

function write(path, content = "") {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, content);
}

function fixture() {
  const root = mkdtempSync(join(tmpdir(), "techstack-deps-ready-"));
  write(join(root, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n");
  write(
    join(root, "node_modules", ".pnpm", "lock.yaml"),
    "lockfileVersion: '9.0'\n",
  );
  write(join(root, "node_modules", ".modules.yaml"), "{}\n");
  write(join(root, "node_modules", ".bin", "vite"), "launcher\n");
  write(join(root, "node_modules", ".bin", "vitest"), "launcher\n");
  write(join(root, "node_modules", ".bin", "svelte-check"), "launcher\n");
  return root;
}

test("dependency readiness requires an exact installed lock, manifest proof, and required tools", () => {
  const root = fixture();
  assert.equal(
    dependenciesReady(root, () => true),
    true,
  );
  assert.equal(
    dependenciesReady(root, () => false),
    false,
  );

  write(
    join(root, "pnpm-lock.yaml"),
    "lockfileVersion: '9.0'\nchanged: true\n",
  );
  assert.equal(
    dependenciesReady(root, () => true),
    false,
  );
});

test("dependency readiness fails when installed module metadata is missing", () => {
  const root = mkdtempSync(join(tmpdir(), "techstack-deps-missing-"));
  write(join(root, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n");
  assert.equal(
    dependenciesReady(root, () => true),
    false,
  );
});

test("dependency readiness rejects empty or missing launchers from a partial install", () => {
  const root = fixture();
  write(join(root, "node_modules", ".bin", "vite"), "");
  assert.equal(
    dependenciesReady(root, () => true),
    false,
  );
});

test("frozen manifest verification is offline, immutable, and rejects pnpm failure", () => {
  const root = fixture();
  let invocation;
  assert.equal(
    frozenManifestReady(root, (command, args, options) => {
      invocation = { command, args, options };
      return { status: 0 };
    }),
    true,
  );
  assert.ok(invocation.args.includes("--offline"));
  assert.ok(invocation.args.includes("--frozen-lockfile"));
  assert.ok(invocation.args.includes("--lockfile-only"));
  assert.ok(invocation.args.includes("--ignore-scripts"));
  assert.equal(
    frozenManifestReady(root, () => ({ status: 1 })),
    false,
  );
});
