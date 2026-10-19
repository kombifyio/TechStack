// Deterministic locale gate: every locale dictionary of the web app carries
// exactly the English keys with identical interpolation tokens, every literal
// key the app source passes to a translation helper exists in the English
// catalog, and every Windows client .resx culture carries the neutral file's
// names and {n} tokens.
import fs from "node:fs";
import path from "node:path";
import "./i18n-ts-loader.mjs";

const { translations, getAvailableLocales } =
  await import("../src/lib/i18n.ts");

const tokens = (value) => (value.match(/\{[^{}]*\}/g) ?? []).sort().join("|");
const failures = [];
const report = [];

const en = translations.en;
for (const { code } of getAvailableLocales()) {
  const dict = translations[code];
  const keys = Object.keys(dict);
  report.push(`web ${code}: ${keys.length} keys`);
  if (code === "en") continue;
  for (const key of Object.keys(en)) {
    if (typeof dict[key] !== "string" || dict[key].trim() === "") {
      failures.push(`web ${code}: missing ${key}`);
    } else if (tokens(dict[key]) !== tokens(en[key])) {
      failures.push(`web ${code}: placeholder mismatch ${key}`);
    }
  }
  for (const key of keys) {
    if (!(key in en)) failures.push(`web ${code}: extra ${key}`);
  }
}

// A literal key passed to tr/t/trn/trParts must exist. Without this a missing
// key renders as the raw key in the product ("common.retry"), which no locale
// parity check can see because the key is absent from every locale alike.
const MESSAGE_KEY = /^[A-Za-z][\w-]*(\.[\w-]+)+$/;
const HELPER_CALL = /(?<![\w$.])(tr|t|trn|trParts)\(\s*/g;
const sourceRoot = path.resolve(import.meta.dirname, "../src");

function* sourceFiles(dir) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const file = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (entry.name !== "node_modules" && entry.name !== ".svelte-kit")
        yield* sourceFiles(file);
    } else if (
      /\.(ts|js|svelte)$/.test(entry.name) &&
      !/\.(test|spec|d)\.(ts|js)$/.test(entry.name)
    ) {
      yield file;
    }
  }
}

/**
 * The string literals of the first argument expression that starts at
 * `start`: the literal itself when the argument is one bare literal, only the
 * key-shaped ones when the expression chooses between several
 * (`cond ? "a.b" : "c.d"`, `x ?? "e.f"`), so an English fallback sentence is
 * never mistaken for a key. Template literals with interpolation are dynamic
 * and skipped.
 */
function firstArgumentLiterals(text, start) {
  const found = [];
  let depth = 0;
  let i = start;
  while (i < text.length) {
    const char = text[i];
    if (char === '"' || char === "'" || char === "`") {
      let j = i + 1;
      let dynamic = false;
      while (j < text.length && text[j] !== char) {
        if (text[j] === "\\") j++;
        else if (char === "`" && text[j] === "$" && text[j + 1] === "{")
          dynamic = true;
        j++;
      }
      if (!dynamic) found.push(text.slice(i + 1, j));
      i = j + 1;
      continue;
    }
    if (char === "(" || char === "[" || char === "{") depth++;
    else if (char === ")" || char === "]" || char === "}") {
      if (depth === 0) break;
      depth--;
    } else if (char === "," && depth === 0) break;
    i++;
  }
  const argument = text.slice(start, i).trim();
  const bare = found.length === 1 && argument.length === found[0].length + 2;
  return bare ? found : found.filter((value) => MESSAGE_KEY.test(value));
}

const missingKeys = new Map();
for (const file of sourceFiles(sourceRoot)) {
  const text = fs.readFileSync(file, "utf8");
  for (const call of text.matchAll(HELPER_CALL)) {
    const line = text.slice(0, call.index).split("\n").length;
    const literals = firstArgumentLiterals(text, call.index + call[0].length);
    for (const key of literals) {
      const candidates = call[1] === "trn" ? [`${key}.other`] : [key];
      if (candidates.some((candidate) => candidate in en)) continue;
      const where = `${path.relative(sourceRoot, file)}:${line}`;
      missingKeys.set(`${key}@${where}`, `${call[1]}("${key}") at ${where}`);
    }
  }
}
report.push(`source: ${missingKeys.size} literal keys without catalog entry`);
for (const usage of missingKeys.values())
  failures.push(`web en: no catalog entry for ${usage}`);

const clientDir = path.resolve(
  import.meta.dirname,
  "../../clients/windows/Kombify.TechStack.Client",
);
const readResx = (file) =>
  new Map(
    [
      ...fs
        .readFileSync(path.join(clientDir, file), "utf8")
        .matchAll(
          /<data name="([^"]+)"[^>]*><value>([\s\S]*?)<\/value><\/data>/g,
        ),
    ].map((m) => [m[1], m[2]]),
  );
for (const file of fs
  .readdirSync(clientDir)
  .filter((f) => /^[A-Za-z]+\.resx$/.test(f))) {
  const base = file.replace(".resx", "");
  const neutral = readResx(file);
  for (const variant of fs
    .readdirSync(clientDir)
    .filter(
      (f) => f.startsWith(`${base}.`) && f !== file && f.endsWith(".resx"),
    )) {
    const culture = variant.slice(base.length + 1, -".resx".length);
    const own = readResx(variant);
    report.push(`resx ${base} ${culture}: ${own.size} names`);
    for (const [name, value] of neutral) {
      if (!own.has(name) || own.get(name).trim() === "") {
        failures.push(`resx ${base}.${culture}: missing ${name}`);
      } else if (tokens(own.get(name)) !== tokens(value)) {
        failures.push(`resx ${base}.${culture}: placeholder mismatch ${name}`);
      }
    }
    for (const name of own.keys()) {
      if (!neutral.has(name))
        failures.push(`resx ${base}.${culture}: extra ${name}`);
    }
  }
}

if (process.argv.includes("--counts")) console.log(report.join("\n"));
if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("i18n parity ok");
