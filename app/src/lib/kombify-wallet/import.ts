/**
 * Import into the kombify Wallet from the files Techstack's own export writes
 * (JSON, CSV) and from a Bitwarden JSON export. Parsing and mapping are pure;
 * the dialog creates the items in the browser, one at a time, and reports per
 * row. A parsed secret lives only in this module's return values and is
 * dropped by the caller after use.
 */
import {
  DESCRIPTION_MAX,
  DISPLAY_NAME_MAX,
  validDescription,
  validDisplayName,
  type EditorItemType,
} from "./item-types.js";
import { MAX_VALUE_BYTES } from "./transport.js";

/**
 * The credential kinds and portable row of a Techstack export file. Declared
 * here, in the shape the export writes, so the kombify Wallet does not depend
 * on the self-hosted Wallet module to read one.
 */
export type CredentialType =
  | "password"
  | "api_key"
  | "ssh_key"
  | "oauth_token"
  | "certificate"
  | "other";

export interface ExportableCredential {
  name: string;
  kind: CredentialType;
  username?: string;
  secret?: string;
  totp?: string;
  url?: string;
  notes?: string;
  expires_at?: string;
  auto_generated?: boolean;
}

export type ImportFormat = "techstack" | "bitwarden" | "csv";

/** Which export a file is, from its content alone. */
export function detectImportFormat(text: string): ImportFormat {
  const trimmed = text.trimStart();
  if (trimmed.startsWith("{")) {
    try {
      const parsed = JSON.parse(trimmed) as Record<string, unknown>;
      if (Array.isArray(parsed.items)) return "bitwarden";
    } catch {
      /* fall through: reported by the format's own parser */
    }
    return "techstack";
  }
  return "csv";
}

const KINDS: readonly CredentialType[] = [
  "password",
  "api_key",
  "ssh_key",
  "oauth_token",
  "certificate",
  "other",
];

function kindOf(value: string | undefined): CredentialType {
  return KINDS.find((kind) => kind === value?.trim().toLowerCase()) ?? "other";
}

/** RFC 4180 rows, including quoted commas, quotes and line breaks. */
export function parseCsvRows(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let cell = "";
  let quoted = false;
  const input = text.replace(/^\uFEFF/, "");
  for (let index = 0; index < input.length; index += 1) {
    const char = input[index];
    if (quoted) {
      if (char === '"' && input[index + 1] === '"') {
        cell += '"';
        index += 1;
      } else if (char === '"') quoted = false;
      else cell += char;
    } else if (char === '"') quoted = true;
    else if (char === ",") {
      row.push(cell);
      cell = "";
    } else if (char === "\n" || char === "\r") {
      if (char === "\r" && input[index + 1] === "\n") index += 1;
      row.push(cell);
      cell = "";
      if (row.some((value) => value !== "")) rows.push(row);
      row = [];
    } else cell += char;
  }
  row.push(cell);
  if (row.some((value) => value !== "")) rows.push(row);
  return rows;
}

/** The CSV Techstack writes: Group,Title,Username,Password,URL,Notes,TOTP. */
export function parseCsvCredentials(text: string): ExportableCredential[] {
  const [header, ...rows] = parseCsvRows(text);
  if (!header) return [];
  const column = (name: string) =>
    header.findIndex((cell) => cell.trim().toLowerCase() === name);
  const at = {
    group: column("group"),
    title: column("title"),
    username: column("username"),
    password: column("password"),
    url: column("url"),
    notes: column("notes"),
    totp: column("totp"),
  };
  if (at.title < 0 || at.password < 0) return [];
  const cell = (row: string[], index: number) =>
    index < 0 ? undefined : row[index] || undefined;
  return rows.map((row) => ({
    name: row[at.title] ?? "",
    kind: kindOf(cell(row, at.group)),
    username: cell(row, at.username),
    secret: cell(row, at.password),
    totp: cell(row, at.totp),
    url: cell(row, at.url),
    notes: cell(row, at.notes),
  }));
}

interface BitwardenItem {
  type?: number;
  name?: string;
  notes?: string | null;
  login?: {
    username?: string | null;
    password?: string | null;
    totp?: string | null;
    uris?: { uri?: string }[] | null;
  };
}

/** A Bitwarden JSON export: logins become passwords, notes become secrets. */
export function parseBitwardenCredentials(
  text: string,
): ExportableCredential[] {
  const parsed = JSON.parse(text) as { items?: BitwardenItem[] };
  if (!Array.isArray(parsed.items)) return [];
  return parsed.items.map((item): ExportableCredential => {
    if (item.type === 1 && item.login) {
      return {
        name: item.name ?? "",
        kind: "password",
        username: item.login.username || undefined,
        secret: item.login.password || undefined,
        totp: item.login.totp || undefined,
        url: item.login.uris?.[0]?.uri || undefined,
        notes: item.notes || undefined,
      };
    }
    return {
      name: item.name ?? "",
      kind: "other",
      secret: item.notes || undefined,
    };
  });
}

export interface ImportableItem {
  itemType: EditorItemType;
  displayName: string;
  description?: string;
  value: string;
}

export type ImportRowProblem = "name_invalid" | "no_secret" | "too_large";

const singleLine = (text: string) => text.replace(/\s+/g, " ").trim();

/** One credential as the Wallet item it becomes, or why it cannot be one. */
export function importableItem(
  credential: ExportableCredential,
): ImportableItem | { problem: ImportRowProblem } {
  const displayName = [...singleLine(credential.name ?? "")]
    .slice(0, DISPLAY_NAME_MAX)
    .join("")
    .trim();
  if (!validDisplayName(displayName)) return { problem: "name_invalid" };
  const secret = credential.secret ?? "";
  if (!secret) return { problem: "no_secret" };
  const description = [
    ...singleLine(
      [credential.url, credential.notes].filter(Boolean).join(" - "),
    ),
  ]
    .slice(0, DESCRIPTION_MAX)
    .join("")
    .trim();
  const common = {
    displayName,
    ...(validDescription(description) ? { description } : {}),
  };
  const username = credential.username?.trim();
  const totp = credential.totp?.trim();
  const login = credential.kind === "password" && Boolean(username);
  const value = login
    ? JSON.stringify({
        username,
        password: secret,
        ...(totp ? { totp } : {}),
      })
    : secret;
  if (new TextEncoder().encode(value).byteLength > MAX_VALUE_BYTES)
    return { problem: "too_large" };
  return { itemType: login ? "login" : "generic_secret", value, ...common };
}
