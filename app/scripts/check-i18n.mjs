// Deterministic locale gate: every locale dictionary of the web app carries
// exactly the English keys with identical interpolation tokens, and every
// Windows client .resx culture carries the neutral file's names and {n} tokens.
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
