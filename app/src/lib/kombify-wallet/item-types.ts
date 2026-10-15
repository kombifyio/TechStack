/**
 * Wallet item types the Techstack editor creates, and how each type's form
 * fields become the one value the browser encrypts. An SSH certificate
 * authority is generated inside the Wallet unless the owner imports a key.
 * Other store types (parent credentials, AI credentials, OAuth connections)
 * are listed and managed but not created here.
 */
import { caValue, normalizeCaPrivateKey, SSH_CA } from "./ssh-ca.js";

/** Wallet item ids (`itm_` plus 26 base32 characters). */
export const WALLET_ITEM_ID = /^itm_[a-z2-7]{26}$/;

export const EDITOR_ITEM_TYPES = [
  "generic_secret",
  "login",
  "variable",
  "configuration",
  "node_access_profile",
  "ssh_ca",
] as const;
export type EditorItemType = (typeof EDITOR_ITEM_TYPES)[number];

export function isEditorItemType(value: unknown): value is EditorItemType {
  return (
    typeof value === "string" &&
    (EDITOR_ITEM_TYPES as readonly string[]).includes(value)
  );
}

/**
 * Types whose value can be replaced by a new version. A CA's key is the trust
 * servers install, so a CA is replaced by a new item, never versioned.
 */
export function canAddVersion(itemType: string): boolean {
  return isEditorItemType(itemType) && itemType !== SSH_CA;
}

/** Field names a type's value is made of. */
export type ValueFieldName =
  | "secret"
  | "username"
  | "password"
  | "totp"
  | "value"
  | "document"
  | "host"
  | "port"
  | "user"
  | "privateKey"
  | "publicKey";

export type NodeAuthMethod = "privateKey" | "password";
/** Where an SSH CA's key comes from: made by the Wallet, or imported. */
export type CaSource = "generate" | "import";

export interface ValueDraft {
  secret: string;
  username: string;
  password: string;
  totp: string;
  value: string;
  document: string;
  host: string;
  /** Server access only; empty means the SSH default. */
  port: string;
  user: string;
  privateKey: string;
  /** Server access with a private key: its OpenSSH public line, when known. */
  publicKey: string;
  nodeAuth: NodeAuthMethod;
  caSource: CaSource;
}

export function emptyValueDraft(): ValueDraft {
  return {
    secret: "",
    username: "",
    password: "",
    totp: "",
    value: "",
    document: "",
    host: "",
    port: "",
    user: "",
    privateKey: "",
    publicKey: "",
    nodeAuth: "privateKey",
    caSource: "generate",
  };
}

/** Overwrite every field so the draft holds no value after use. */
export function clearValueDraft(draft: ValueDraft): void {
  const empty = emptyValueDraft();
  for (const key of Object.keys(empty) as (keyof ValueDraft)[]) {
    if (key === "nodeAuth" || key === "caSource") continue;
    draft[key] = "";
  }
}

export type ValueError =
  | "required"
  | "host_invalid"
  | "port_invalid"
  | "public_key_invalid"
  | "ca_key_invalid"
  | "ca_key_encrypted";

/** An OpenSSH public key line: type, base64 body, optional comment. */
const OPENSSH_PUBLIC_LINE =
  /^(ssh-ed25519|ecdsa-sha2-nistp(?:256|384|521)|ssh-rsa) [A-Za-z0-9+/]+={0,2}(?: [^\r\n]*)?$/;

/**
 * Compose the value to encrypt. Login and server access are JSON documents
 * (`{username,password,totp?}`, `{host,user,port?,privateKey,publicKey?|password}`); the other
 * types are their single field as entered. An SSH CA has a value only when its
 * key is imported (`{algorithm,privateKey}`); a generated CA has none.
 */
export function composeValue(
  type: EditorItemType,
  draft: ValueDraft,
): { value: string } | { error: ValueError; field: ValueFieldName } {
  switch (type) {
    case "generic_secret":
      return draft.secret.length > 0
        ? { value: draft.secret }
        : { error: "required", field: "secret" };
    case "variable":
      return draft.value.length > 0
        ? { value: draft.value }
        : { error: "required", field: "value" };
    case "configuration":
      return draft.document.length > 0
        ? { value: draft.document }
        : { error: "required", field: "document" };
    case "login": {
      const username = draft.username.trim();
      if (!username) return { error: "required", field: "username" };
      if (!draft.password) return { error: "required", field: "password" };
      const totp = draft.totp.trim();
      return {
        value: JSON.stringify({
          username,
          password: draft.password,
          ...(totp ? { totp } : {}),
        }),
      };
    }
    case "node_access_profile": {
      const host = draft.host.trim();
      if (!host) return { error: "required", field: "host" };
      if (/\s/.test(host) || host.length > 255)
        return { error: "host_invalid", field: "host" };
      const user = draft.user.trim();
      if (!user) return { error: "required", field: "user" };
      const portText = draft.port.trim();
      let port: number | undefined;
      if (portText) {
        port = Number(portText);
        if (!/^[0-9]{1,5}$/.test(portText) || port < 1 || port > 65_535)
          return { error: "port_invalid", field: "port" };
      }
      const access = { host, user, ...(port === undefined ? {} : { port }) };
      if (draft.nodeAuth === "privateKey") {
        if (!draft.privateKey.trim())
          return { error: "required", field: "privateKey" };
        const publicKey = draft.publicKey.trim();
        if (publicKey && !OPENSSH_PUBLIC_LINE.test(publicKey))
          return { error: "public_key_invalid", field: "publicKey" };
        return {
          value: JSON.stringify({
            ...access,
            privateKey: draft.privateKey,
            ...(publicKey ? { publicKey } : {}),
          }),
        };
      }
      if (!draft.password) return { error: "required", field: "password" };
      return {
        value: JSON.stringify({ ...access, password: draft.password }),
      };
    }
    case "ssh_ca": {
      const key = normalizeCaPrivateKey(draft.privateKey);
      if ("privateKey" in key) return { value: caValue(key.privateKey) };
      return {
        error:
          key.error === "required"
            ? "required"
            : key.error === "encrypted"
              ? "ca_key_encrypted"
              : "ca_key_invalid",
        field: "privateKey",
      };
    }
  }
}

/** A revealed value split into labelled parts for display. */
export interface RevealedPart {
  field: ValueFieldName;
  value: string;
}

const JSON_FIELDS: Record<string, ValueFieldName[]> = {
  login: ["username", "password", "totp"],
  node_access_profile: [
    "host",
    "port",
    "user",
    "privateKey",
    "publicKey",
    "password",
  ],
};

/**
 * Split a revealed value into its fields for login and server access; any
 * other type, or a value that is not the expected document, shows whole.
 */
export function revealedParts(itemType: string, value: string): RevealedPart[] {
  const fields = JSON_FIELDS[itemType];
  if (fields) {
    try {
      const parsed: unknown = JSON.parse(value);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        const record = parsed as Record<string, unknown>;
        const parts = fields
          .filter(
            (field) =>
              typeof record[field] === "string" ||
              (field === "port" && typeof record[field] === "number"),
          )
          .map((field) => ({ field, value: String(record[field]) }));
        if (parts.length > 0) return parts;
      }
    } catch {
      /* not a document: show the value whole */
    }
  }
  return [
    {
      field:
        itemType === "configuration"
          ? "document"
          : itemType === "variable"
            ? "value"
            : "secret",
      value,
    },
  ];
}

/** `account`, `project:<id>` or `environment:<project>/<env>`. */
export type ScopeKind = "account" | "project" | "environment";
const SEGMENT = /^[A-Za-z0-9._-]{1,64}$/;

export function composeScope(
  kind: ScopeKind,
  project: string,
  environment: string,
): { scope: string } | { error: "project_invalid" | "environment_invalid" } {
  if (kind === "account") return { scope: "account" };
  const projectId = project.trim();
  if (!SEGMENT.test(projectId)) return { error: "project_invalid" };
  if (kind === "project") return { scope: `project:${projectId}` };
  const env = environment.trim();
  if (!SEGMENT.test(env)) return { error: "environment_invalid" };
  return { scope: `environment:${projectId}/${env}` };
}

export function parseScope(
  scope: string,
):
  | { kind: "account" }
  | { kind: "project"; project: string }
  | { kind: "environment"; project: string; environment: string }
  | null {
  if (scope === "account") return { kind: "account" };
  const project = /^project:([A-Za-z0-9._-]{1,64})$/.exec(scope);
  if (project) return { kind: "project", project: project[1] };
  const env =
    /^environment:([A-Za-z0-9._-]{1,64})\/([A-Za-z0-9._-]{1,64})$/.exec(scope);
  if (env) return { kind: "environment", project: env[1], environment: env[2] };
  return null;
}

/**
 * Variables and configurations carry a `name` the Wallet API requires and
 * keeps unique per type within one scope; resolution looks items up by it.
 * Every other type is refused a name. The grammars are the Wallet Store's.
 */
export const ITEM_NAME_PATTERNS = {
  variable: /^[A-Z_][A-Z0-9_]{0,127}$/,
  configuration: /^[a-z0-9][a-z0-9._-]{0,127}$/,
} as const;
export type NamedItemType = keyof typeof ITEM_NAME_PATTERNS;

export function isNamedItemType(value: unknown): value is NamedItemType {
  return typeof value === "string" && Object.hasOwn(ITEM_NAME_PATTERNS, value);
}

export function validItemName(itemType: NamedItemType, value: string): boolean {
  return ITEM_NAME_PATTERNS[itemType].test(value);
}

/** Display name and description rules of the Wallet API. */
export const DISPLAY_NAME_MAX = 120;
export const DESCRIPTION_MAX = 500;

// The Wallet API refuses control characters in display text; these patterns
// name them on purpose.
/* eslint-disable no-control-regex */
const SINGLE_LINE_CONTROL = /[\u0000-\u001f\u007f-\u009f]/u;
const MULTI_LINE_CONTROL = /[\u0000-\u0009\u000b-\u001f\u007f-\u009f]/u;
/* eslint-enable no-control-regex */

export function validDisplayName(value: string): boolean {
  const length = [...value].length;
  return (
    length >= 1 &&
    length <= DISPLAY_NAME_MAX &&
    value.trim() === value &&
    !SINGLE_LINE_CONTROL.test(value)
  );
}

export function validDescription(value: string): boolean {
  const length = [...value].length;
  return (
    length >= 1 &&
    length <= DESCRIPTION_MAX &&
    value.trim() === value &&
    !MULTI_LINE_CONTROL.test(value)
  );
}

// --- server access facts ------------------------------------------------------

/**
 * What a server access item says about itself without being opened: the Wallet
 * list never carries values, so the item's description starts with
 * `ssh <user>@<host>[:<port>] (key|password)` when this editor created it. The
 * picker uses it to offer the items that fit a server; Techstack's backend
 * stays the authority and refuses a mismatch (`wallet_item_host_mismatch`,
 * `wallet_item_credential_mismatch`).
 */
export interface NodeAccessFacts {
  host: string;
  user: string;
  port?: number;
  method: NodeAuthMethod;
}

const ACCESS_PREFIX =
  /^ssh ([^@\s]+)@(\[[0-9A-Fa-f:.]+\]|[^\s:@]+)(?::([0-9]{1,5}))? \((key|password)\)(?: - |$)/;

/** The description line of a server access item, ahead of any note. */
export function describeNodeAccess(facts: NodeAccessFacts, note = ""): string {
  const port = facts.port && facts.port !== 22 ? `:${facts.port}` : "";
  const host = facts.host.includes(":") ? `[${facts.host}]` : facts.host;
  const method = facts.method === "password" ? "password" : "key";
  const line = `ssh ${facts.user}@${host}${port} (${method})`;
  return note ? `${line} - ${note}` : line;
}

/** The facts a server access item's description records, or null when it records none. */
export function nodeAccessFacts(item: {
  itemType: string;
  description?: string;
}): NodeAccessFacts | null {
  if (item.itemType !== "node_access_profile" || !item.description) return null;
  const match = ACCESS_PREFIX.exec(item.description);
  if (!match) return null;
  return {
    user: match[1],
    host: match[2].replace(/^\[|\]$/g, ""),
    ...(match[3] ? { port: Number(match[3]) } : {}),
    method: match[4] === "password" ? "password" : "privateKey",
  };
}

/** Whether two hosts name the same server (case, brackets and a trailing dot aside). */
export function sameHost(left: string, right: string): boolean {
  const normal = (host: string) =>
    host
      .trim()
      .toLowerCase()
      .replace(/^\[|\]$/g, "")
      .replace(/\.$/, "");
  return normal(left) !== "" && normal(left) === normal(right);
}
