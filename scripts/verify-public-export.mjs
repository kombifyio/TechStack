#!/usr/bin/env node

// Recompute the export digest of a public checkout and compare it with the
// receipt the exporter wrote. Usage: node scripts/verify-public-export.mjs [dir]
import { createHash } from "node:crypto";
import { readFileSync, readdirSync } from "node:fs";
import { join, resolve } from "node:path";

const receiptPath = ".techstack-public-export.json";
const root = resolve(process.argv[2] || ".");
const receipt = JSON.parse(readFileSync(join(root, receiptPath), "utf8"));

const files = [];
function walk(relative) {
  for (const entry of readdirSync(join(root, relative), { withFileTypes: true })) {
    const path = relative ? `${relative}/${entry.name}` : entry.name;
    if (path === ".git" || path === receiptPath) continue;
    if (entry.isDirectory()) walk(path);
    else if (entry.isFile()) files.push(path);
    else throw new Error(`unexpected non-regular entry: ${path}`);
  }
}
walk("");
files.sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));

const digest = createHash("sha256");
for (const path of files) {
  digest.update(path);
  digest.update("\0");
  digest.update(readFileSync(join(root, ...path.split("/"))));
  digest.update("\0");
}
const actual = digest.digest("hex");
const ok =
  actual === receipt.candidate_digest &&
  files.length === receipt.candidate_file_count;
console.log(
  `${ok ? "MATCH" : "MISMATCH"} source_commit=${receipt.source_commit} files=${files.length}/${receipt.candidate_file_count} digest=${actual}`,
);
process.exitCode = ok ? 0 : 1;
