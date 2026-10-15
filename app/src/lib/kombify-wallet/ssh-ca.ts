/**
 * SSH certificate authority items (Wallet plan P3.1). The grammar mirrors the
 * Gateway contract (`services/wallet-store/AGENT-ACCESS.md`, "SSH
 * certificates", and `src/ssh-certificate.ts`) so the editor refuses what the
 * Wallet Store would refuse; the store stays the authority.
 *
 * The CA's private key never reaches kombify Cloud: the Wallet generates it,
 * or the owner imports one through the normal browser-side capture. A pasted
 * subject key that looks like a private key is refused here and never leaves
 * the browser.
 */

export const SSH_CA = "ssh_ca";

export function isSshCa(itemType: string): boolean {
  return itemType === SSH_CA;
}

export const CERTIFICATE_KINDS = ["user", "host"] as const;
export type CertificateKind = (typeof CERTIFICATE_KINDS)[number];

export const USER_EXTENSIONS = [
  "permit-X11-forwarding",
  "permit-agent-forwarding",
  "permit-port-forwarding",
  "permit-pty",
  "permit-user-rc",
] as const;
export type UserExtension = (typeof USER_EXTENSIONS)[number];

export const MIN_SECONDS = 60;
export const MAX_USER_SECONDS = 86_400;
export const MAX_HOST_SECONDS = 366 * 86_400;
export const MAX_POLICY_PRINCIPALS = 64;
export const MAX_SIGN_PRINCIPALS = 32;
const SIGN_POLICY_MAX_BYTES = 16_384;
const SOURCE_MAX_CHARS = 1_024;
const MAX_SOURCES = 16;
const PUBLIC_LINE_MAX_CHARS = 8_192;

/** What the editor offers when a policy is first written: user certificates, 8 hours. */
export const DEFAULT_MAX_USER_SECONDS = 8 * 3_600;
export const DEFAULT_MAX_HOST_SECONDS = 90 * 86_400;
const DEFAULT_SIGN_SECONDS = 3_600;

/** The owner's upper bound on what a CA signs (`signPolicy`). */
export interface SignPolicy {
  kinds: CertificateKind[];
  principals: string[];
  maxUserSeconds: number;
  maxHostSeconds: number;
  userExtensions: UserExtension[];
  requireSourceAddress?: boolean;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function exactKeys(value: Record<string, unknown>, keys: readonly string[]) {
  return (
    Object.keys(value).length === keys.length &&
    keys.every((key) => Object.hasOwn(value, key))
  );
}

function distinct<T>(
  value: unknown,
  minimum: number,
  maximum: number,
  valid: (member: unknown) => member is T,
): value is T[] {
  return (
    Array.isArray(value) &&
    value.length >= minimum &&
    value.length <= maximum &&
    value.every(valid) &&
    new Set(value).size === value.length
  );
}

const integer = (value: unknown, minimum: number, maximum: number) =>
  Number.isSafeInteger(value) &&
  (value as number) >= minimum &&
  (value as number) <= maximum;

const isKind = (value: unknown): value is CertificateKind =>
  (CERTIFICATE_KINDS as readonly unknown[]).includes(value);
const isExtension = (value: unknown): value is UserExtension =>
  (USER_EXTENSIONS as readonly unknown[]).includes(value);

/** A principal: no whitespace, comma or sshd pattern character. */
const NAME = /^[A-Za-z0-9_][A-Za-z0-9._-]{0,252}$/;
const LABEL = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/;

/** Exact name, or `*.<suffix>` where `*` is exactly one leading label. */
export function isPrincipalPattern(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value.length <= 253 &&
    (NAME.test(value) || (value.startsWith("*.") && NAME.test(value.slice(2))))
  );
}

const isHostName = (value: string) =>
  value.length <= 253 && value.split(".").every((label) => LABEL.test(label));

/** Exact match, or `*.<suffix>` covering exactly one more leading label. */
export function principalCovered(pattern: string, name: string): boolean {
  if (!pattern.startsWith("*.")) return pattern === name;
  const suffix = pattern.slice(1);
  const label = name.slice(0, name.length - suffix.length);
  return name.endsWith(suffix) && label.length > 0 && !label.includes(".");
}

export function isSignPolicy(value: unknown): value is SignPolicy {
  if (!isRecord(value)) return false;
  const optional = Object.hasOwn(value, "requireSourceAddress");
  return (
    exactKeys(value, [
      "kinds",
      "principals",
      "maxUserSeconds",
      "maxHostSeconds",
      "userExtensions",
      ...(optional ? ["requireSourceAddress"] : []),
    ]) &&
    distinct(value.kinds, 1, CERTIFICATE_KINDS.length, isKind) &&
    distinct(value.principals, 1, MAX_POLICY_PRINCIPALS, isPrincipalPattern) &&
    integer(value.maxUserSeconds, MIN_SECONDS, MAX_USER_SECONDS) &&
    integer(value.maxHostSeconds, MIN_SECONDS, MAX_HOST_SECONDS) &&
    distinct(value.userExtensions, 0, USER_EXTENSIONS.length, isExtension) &&
    (!optional || typeof value.requireSourceAddress === "boolean") &&
    new TextEncoder().encode(JSON.stringify(value)).byteLength <=
      SIGN_POLICY_MAX_BYTES
  );
}

/** The CA settings as the owner list reports them. */
export function itemSignPolicy(item: {
  signPolicy?: unknown;
}): SignPolicy | null {
  return isSignPolicy(item.signPolicy) ? item.signPolicy : null;
}

const CA_PUBLIC_KEY = /^ssh-ed25519 [A-Za-z0-9+/]{1,200}={0,2}$/;

/** The `ssh-ed25519 <base64>` line servers trust; not secret. */
export function itemCaPublicKey(item: {
  caPublicKey?: unknown;
}): string | null {
  return typeof item.caPublicKey === "string" &&
    CA_PUBLIC_KEY.test(item.caPublicKey)
    ? item.caPublicKey
    : null;
}

// --- durations --------------------------------------------------------------

const UNIT_SECONDS = { s: 1, m: 60, h: 3_600, d: 86_400 } as const;

/** `30m`, `8h`, `2d`, `1h30m`; seconds, or null when it is not a duration. */
export function parseDuration(text: string): number | null {
  const compact = text.replace(/\s+/g, "").toLowerCase();
  if (!/^(?:[0-9]{1,6}[smhd])+$/.test(compact)) return null;
  let total = 0;
  for (const [, amount, unit] of compact.matchAll(/([0-9]{1,6})([smhd])/g))
    total += Number(amount) * UNIT_SECONDS[unit as keyof typeof UNIT_SECONDS];
  return Number.isSafeInteger(total) ? total : null;
}

export function formatDuration(seconds: number): string {
  let rest = seconds;
  let text = "";
  for (const [unit, size] of [
    ["d", 86_400],
    ["h", 3_600],
    ["m", 60],
    ["s", 1],
  ] as const) {
    const count = Math.floor(rest / size);
    if (count > 0) {
      text += `${count}${unit}`;
      rest -= count * size;
    }
  }
  return text || "0s";
}

/** Names separated by commas or whitespace, once each, in the order given. */
export function splitList(text: string): string[] {
  return [...new Set(text.split(/[\s,]+/).filter(Boolean))];
}

// --- sign policy form -------------------------------------------------------

export interface SignPolicyDraft {
  user: boolean;
  host: boolean;
  maxUser: string;
  maxHost: string;
  principals: string;
  extensions: UserExtension[];
  requireSourceAddress: boolean;
}

/** Secure defaults: user certificates only, 8 hours, a terminal and nothing more. */
export function emptySignPolicyDraft(): SignPolicyDraft {
  return {
    user: true,
    host: false,
    maxUser: formatDuration(DEFAULT_MAX_USER_SECONDS),
    maxHost: formatDuration(DEFAULT_MAX_HOST_SECONDS),
    principals: "",
    extensions: ["permit-pty"],
    requireSourceAddress: false,
  };
}

export function draftFromSignPolicy(policy: SignPolicy): SignPolicyDraft {
  return {
    user: policy.kinds.includes("user"),
    host: policy.kinds.includes("host"),
    maxUser: formatDuration(policy.maxUserSeconds),
    maxHost: formatDuration(policy.maxHostSeconds),
    principals: policy.principals.join("\n"),
    extensions: [...policy.userExtensions],
    requireSourceAddress: policy.requireSourceAddress === true,
  };
}

export interface SignPolicyErrors {
  kinds?: true;
  principals?: "required" | "invalid" | "too_many";
  /** The first principal that is not valid, for the message. */
  principal?: string;
  maxUser?: true;
  maxHost?: true;
}

/** A lifetime in range; a kind that is switched off keeps a valid text or the default. */
function lifetime(
  text: string,
  maximum: number,
  enabled: boolean,
  fallback: number,
): number | null {
  const seconds = parseDuration(text);
  const valid =
    seconds !== null && seconds >= MIN_SECONDS && seconds <= maximum;
  if (valid) return seconds;
  return enabled ? null : fallback;
}

export function composeSignPolicy(
  draft: SignPolicyDraft,
): { ok: true; policy: SignPolicy } | { ok: false; errors: SignPolicyErrors } {
  const errors: SignPolicyErrors = {};
  const kinds = CERTIFICATE_KINDS.filter((kind) => draft[kind]);
  if (kinds.length === 0) errors.kinds = true;
  const maxUserSeconds = lifetime(
    draft.maxUser,
    MAX_USER_SECONDS,
    draft.user,
    DEFAULT_MAX_USER_SECONDS,
  );
  if (maxUserSeconds === null) errors.maxUser = true;
  const maxHostSeconds = lifetime(
    draft.maxHost,
    MAX_HOST_SECONDS,
    draft.host,
    DEFAULT_MAX_HOST_SECONDS,
  );
  if (maxHostSeconds === null) errors.maxHost = true;
  const principals = splitList(draft.principals);
  if (principals.length === 0) errors.principals = "required";
  else if (principals.length > MAX_POLICY_PRINCIPALS)
    errors.principals = "too_many";
  else {
    const bad = principals.find((name) => !isPrincipalPattern(name));
    if (bad !== undefined) {
      errors.principals = "invalid";
      errors.principal = bad;
    }
  }
  if (Object.keys(errors).length > 0 || !maxUserSeconds || !maxHostSeconds)
    return { ok: false, errors };
  return {
    ok: true,
    policy: {
      kinds: [...kinds],
      principals,
      maxUserSeconds,
      maxHostSeconds,
      userExtensions: draft.user
        ? USER_EXTENSIONS.filter((name) => draft.extensions.includes(name))
        : [],
      ...(draft.user && draft.requireSourceAddress
        ? { requireSourceAddress: true }
        : {}),
    },
  };
}

/** The same bound, whatever the order of its lists or the spelling of its flag. */
export function sameSignPolicy(left: SignPolicy, right: SignPolicy): boolean {
  const normal = (policy: SignPolicy) =>
    JSON.stringify({
      kinds: [...policy.kinds].sort(),
      principals: [...policy.principals].sort(),
      maxUserSeconds: policy.maxUserSeconds,
      maxHostSeconds: policy.maxHostSeconds,
      userExtensions: [...policy.userExtensions].sort(),
      requireSourceAddress: policy.requireSourceAddress === true,
    });
  return normal(left) === normal(right);
}

// --- OpenSSH wire helpers ---------------------------------------------------

const encoder = new TextEncoder();
const decoder = new TextDecoder();

function decodeBase64(text: string): Uint8Array | null {
  if (!/^[A-Za-z0-9+/]+={0,2}$/.test(text) || text.length % 4 !== 0)
    return null;
  try {
    return Uint8Array.from(atob(text), (char) => char.charCodeAt(0));
  } catch {
    return null;
  }
}

/** Reads uint32 and length-prefixed strings; every read is bounded or throws. */
class WireReader {
  #offset = 0;
  constructor(private readonly bytes: Uint8Array) {}
  get remaining(): number {
    return this.bytes.length - this.#offset;
  }
  take(length: number): Uint8Array {
    if (length < 0 || length > this.remaining) throw new Error("short");
    const value = this.bytes.subarray(this.#offset, this.#offset + length);
    this.#offset += length;
    return value;
  }
  u32(): number {
    const [a, b, c, d] = this.take(4);
    return a * 0x1000000 + (b << 16) + (c << 8) + d;
  }
  string(): Uint8Array {
    return this.take(this.u32());
  }
  text(): string {
    return decoder.decode(this.string());
  }
}

const sameBytes = (left: Uint8Array, right: Uint8Array) =>
  left.length === right.length && left.every((byte, i) => byte === right[i]);

// --- importing an existing CA key ------------------------------------------

export type CaKeyError = "required" | "invalid" | "encrypted";

const MAGIC = encoder.encode("openssh-key-v1\0");
const CA_PEM =
  /^-----BEGIN OPENSSH PRIVATE KEY-----\n([\s\S]*?)\n?-----END OPENSSH PRIVATE KEY-----$/;

/**
 * An unencrypted OpenSSH Ed25519 private key as the Wallet takes it, with its
 * lines rewrapped. This only gives early, specific advice; the store checks
 * the key again when it opens the capture.
 */
export function normalizeCaPrivateKey(
  text: string,
): { privateKey: string } | { error: CaKeyError } {
  const trimmed = text.replace(/\r\n?/g, "\n").trim();
  if (!trimmed) return { error: "required" };
  const pem = CA_PEM.exec(trimmed);
  if (!pem) return { error: "invalid" };
  const body = pem[1].replace(/\s+/g, "");
  const bytes = decodeBase64(body);
  if (!bytes) return { error: "invalid" };
  try {
    const reader = new WireReader(bytes);
    if (!sameBytes(reader.take(MAGIC.length), MAGIC))
      return { error: "invalid" };
    if (reader.text() !== "none") return { error: "encrypted" };
    reader.string();
    reader.string();
    if (reader.u32() !== 1) return { error: "invalid" };
    const publicKey = new WireReader(reader.string());
    if (publicKey.text() !== "ssh-ed25519") return { error: "invalid" };
  } catch {
    return { error: "invalid" };
  }
  return {
    privateKey: `-----BEGIN OPENSSH PRIVATE KEY-----\n${body.match(/.{1,70}/g)!.join("\n")}\n-----END OPENSSH PRIVATE KEY-----\n`,
  };
}

/** The value the Wallet captures for an imported CA. */
export function caValue(privateKey: string): string {
  return JSON.stringify({ algorithm: "ssh-ed25519", privateKey });
}

// --- signing a certificate --------------------------------------------------

export type PublicKeyError = "required" | "private_key" | "invalid";

export type SubjectKeyType = "ssh-ed25519" | "ecdsa-sha2-nistp256" | "ssh-rsa";

/** A pasted public key reduced to its canonical line. */
export interface SubjectKey {
  type: SubjectKeyType;
  /** `<type> <base64>`; the comment is dropped. */
  line: string;
  /** The key fields after the type string, as a certificate embeds them. */
  fields: Uint8Array;
}

// A comment may hold any text but control characters.
/* eslint-disable no-control-regex */
const PUBLIC_LINE =
  /^(ssh-ed25519|ecdsa-sha2-nistp256|ssh-rsa) ([A-Za-z0-9+/]+={0,2})(?: [^\u0000-\u001f\u007f]*)?$/;
/* eslint-enable no-control-regex */

/** The base64 start of any openssh-key-v1 private key body. */
const OPENSSH_KEY_BODY = /^b3BlbnNzaC1rZXktdjE/;

/**
 * Read a pasted public key line. Anything that looks like a private key is
 * reported as such so the caller can discard it unsent.
 */
export function inspectPublicKey(
  text: string,
): { key: SubjectKey } | { error: PublicKeyError } {
  const trimmed = text.trim();
  if (!trimmed) return { error: "required" };
  if (
    /-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----/.test(trimmed) ||
    /^PuTTY-User-Key-File/i.test(trimmed) ||
    OPENSSH_KEY_BODY.test(trimmed.replace(/\s+/g, ""))
  )
    return { error: "private_key" };
  if (trimmed.length > PUBLIC_LINE_MAX_CHARS || /[\r\n]/.test(trimmed))
    return { error: "invalid" };
  const match = PUBLIC_LINE.exec(trimmed);
  if (!match) return { error: "invalid" };
  const type = match[1] as SubjectKeyType;
  const blob = decodeBase64(match[2]);
  if (!blob) return { error: "invalid" };
  let fields: Uint8Array;
  try {
    const reader = new WireReader(blob);
    if (reader.text() !== type) return { error: "invalid" };
    fields = reader.take(reader.remaining);
  } catch {
    return { error: "invalid" };
  }
  return { key: { type, line: `${type} ${match[2]}`, fields } };
}

export interface SignDraft {
  publicKey: string;
  kind: CertificateKind;
  principals: string;
  ttl: string;
  sourceAddress: string;
}

/** The body the Wallet signs, and nothing else. */
export interface SignRequest {
  kind: CertificateKind;
  publicKey: string;
  principals: string[];
  ttlSeconds: number;
  sourceAddress?: string;
}

export interface SignErrors {
  publicKey?: PublicKeyError;
  principals?: "required" | "invalid" | "outside" | "too_many";
  /** The first principal that is refused, for the message. */
  principal?: string;
  ttl?: true;
  sourceAddress?: "required" | "invalid";
}

export const maxSeconds = (policy: SignPolicy, kind: CertificateKind) =>
  kind === "user" ? policy.maxUserSeconds : policy.maxHostSeconds;

/** One hour, or less when the policy allows less. */
export function defaultSignDuration(
  policy: SignPolicy,
  kind: CertificateKind,
): string {
  return formatDuration(
    Math.min(DEFAULT_SIGN_SECONDS, maxSeconds(policy, kind)),
  );
}

/** The only principal a policy names, when it is one exact name. */
export function defaultPrincipals(policy: SignPolicy): string {
  return policy.principals.length === 1 &&
    !policy.principals[0].startsWith("*.")
    ? policy.principals[0]
    : "";
}

export function emptySignDraft(
  policy: SignPolicy,
  kind: CertificateKind = policy.kinds.includes("user") ? "user" : "host",
): SignDraft {
  return {
    publicKey: "",
    kind,
    principals: kind === "user" ? defaultPrincipals(policy) : "",
    ttl: defaultSignDuration(policy, kind),
    sourceAddress: "",
  };
}

/** Comma-separated addresses or networks; the store checks them exactly. */
function sourceAddresses(text: string): string | null {
  const entries = text.split(",").map((entry) => entry.trim());
  if (
    entries.length > MAX_SOURCES ||
    new Set(entries).size !== entries.length ||
    !entries.every((entry) => {
      const [address, prefix, ...rest] = entry.split("/");
      return (
        rest.length === 0 &&
        /^[0-9A-Fa-f:.]+$/.test(address) &&
        (address.includes(".") || address.includes(":")) &&
        (prefix === undefined || /^[0-9]{1,3}$/.test(prefix))
      );
    })
  )
    return null;
  const joined = entries.join(",");
  return joined.length <= SOURCE_MAX_CHARS ? joined : null;
}

/**
 * Resolve a draft inside the policy, as the store will: every principal is
 * covered by it, the lifetime is within its bound for the kind and a source
 * address is present when it requires one. Host certificates carry no source
 * address. The dialog offers only the kinds the policy allows.
 */
export function composeSignRequest(
  policy: SignPolicy,
  draft: SignDraft,
):
  | { ok: true; request: SignRequest; subject: SubjectKey }
  | { ok: false; errors: SignErrors; discardKey: boolean } {
  const errors: SignErrors = {};
  const inspected = inspectPublicKey(draft.publicKey);
  if ("error" in inspected) errors.publicKey = inspected.error;
  const principals = splitList(draft.principals);
  if (principals.length === 0) errors.principals = "required";
  else if (principals.length > MAX_SIGN_PRINCIPALS)
    errors.principals = "too_many";
  else {
    const valid = (name: string) =>
      draft.kind === "user" ? NAME.test(name) : isHostName(name);
    const bad = principals.find((name) => !valid(name));
    const outside = principals.find(
      (name) =>
        !policy.principals.some((pattern) => principalCovered(pattern, name)),
    );
    if (bad !== undefined) {
      errors.principals = "invalid";
      errors.principal = bad;
    } else if (outside !== undefined) {
      errors.principals = "outside";
      errors.principal = outside;
    }
  }
  const ttlSeconds = parseDuration(draft.ttl);
  if (
    ttlSeconds === null ||
    ttlSeconds < MIN_SECONDS ||
    ttlSeconds > maxSeconds(policy, draft.kind)
  )
    errors.ttl = true;
  let sourceAddress: string | undefined;
  if (draft.kind === "user") {
    if (draft.sourceAddress.trim()) {
      const parsed = sourceAddresses(draft.sourceAddress.trim());
      if (parsed === null) errors.sourceAddress = "invalid";
      else sourceAddress = parsed;
    } else if (policy.requireSourceAddress) errors.sourceAddress = "required";
  }
  if (
    Object.keys(errors).length > 0 ||
    "error" in inspected ||
    ttlSeconds === null
  )
    return {
      ok: false,
      errors,
      discardKey: errors.publicKey === "private_key",
    };
  return {
    ok: true,
    subject: inspected.key,
    request: {
      kind: draft.kind,
      publicKey: inspected.key.line,
      principals,
      ttlSeconds,
      ...(sourceAddress ? { sourceAddress } : {}),
    },
  };
}

/** Lines the proxy and the Gateway admit as a subject key, sent as is. */
export function isSubjectKeyLine(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value.length <= PUBLIC_LINE_MAX_CHARS &&
    PUBLIC_LINE.test(value.replace(/\r?\n$/, ""))
  );
}

/**
 * Whether a certificate line certifies exactly the key, kind and principals
 * that were asked for: the relay never gets to hand back a certificate for
 * something else. PROTOCOL.certkeys layout: type, nonce, the subject's key
 * fields, serial, certificate type, key id, principals.
 */
export function certificateMatches(
  certificate: string,
  subject: SubjectKey,
  kind: CertificateKind,
  principals: readonly string[],
): boolean {
  try {
    const [type, encoded] = certificate.split(" ");
    if (type !== `${subject.type}-cert-v01@openssh.com`) return false;
    const blob = decodeBase64(encoded ?? "");
    if (!blob) return false;
    const reader = new WireReader(blob);
    if (reader.text() !== type) return false;
    reader.string(); // nonce
    if (!sameBytes(reader.take(subject.fields.length), subject.fields))
      return false;
    reader.take(8); // serial
    if (reader.u32() !== (kind === "user" ? 1 : 2)) return false;
    reader.string(); // key id
    const packed = new WireReader(reader.string());
    const found: string[] = [];
    while (packed.remaining > 0) found.push(packed.text());
    return (
      found.length === principals.length &&
      [...found].sort().join("\n") === [...principals].sort().join("\n")
    );
  } catch {
    return false;
  }
}
