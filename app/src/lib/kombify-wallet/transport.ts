/**
 * Browser side of the kombify Wallet value transport
 * (`kombify.wallet-transport.v1`, Gateway `services/wallet-store/src/transport.mjs`).
 *
 * Values never reach Techstack or the Gateway in plaintext: the browser
 * encrypts a captured value to the Wallet Store's capture key and decrypts a
 * revealed value with an ephemeral key pair it generated itself. The Gateway
 * only relays JWEs.
 *
 * Capture emits a compact JWE (`ECDH-ES+A256KW` / `A256GCM`, see `jwe.ts`)
 * whose protected header carries the authenticated transport context as
 * `wallet_ctx` (base64url of the context JSON, fields in the fixed order
 * below) and marks it critical, exactly as `encryptTransport` produces it.
 * Reveal checks the returned JWE the way `decryptTransport` does, and checks
 * its protected header before any key operation.
 *
 * Nothing here logs, stores or reports a value or a ciphertext. Errors carry a
 * code only.
 */
import {
  base64urlEncode,
  decodeProtectedHeader,
  decryptCompact,
  encryptCompact,
  KEY_MANAGEMENT,
  CONTENT_ENCRYPTION,
  p256Thumbprint,
  type P256PublicJwk,
} from "./jwe.js";

export const TRANSPORT_TYPE = "kombify.wallet-transport.v1";
export const CAPTURE_KEY_FORMAT = "kombify.wallet-capture-key.v1";
/** Store constants of an owner-session reveal (`owner-api.ts`, `lease.ts`). */
export const REVEAL_AUDIENCE = "urn:kombify:wallet:owner-session";
export const REVEAL_RECIPIENT_KID = "owner-session";
export const MAX_VALUE_BYTES = 65_536;
const MAX_ENVELOPE_BYTES = 100_000;
/** Lifetime of a capture or reveal envelope; the store admits at most 300 s. */
export const ENVELOPE_LIFETIME_MS = 120_000;
const MAX_LIFETIME_MS = 300_000;

const CONTEXT_FIELDS = [
  "custodyNamespace",
  "ownerSubject",
  "vaultRef",
  "itemVersion",
  "keyGeneration",
  "operation",
  "operationId",
  "audience",
  "recipientKeyThumbprint",
  "expiresAt",
] as const;
const HEADER_FIELDS = new Set([
  "alg",
  "enc",
  "typ",
  "kid",
  "epk",
  "crit",
  "wallet_ctx",
]);
const OPERATIONS = new Set(["capture", "reveal", "export", "process"]);
const DECIMAL = /^[1-9][0-9]{0,18}$/;
const THUMBPRINT = /^[A-Za-z0-9_-]{43}$/;
const COORDINATE = /^[A-Za-z0-9_-]{43}$/;
const KID = /^[A-Za-z0-9._:-]{1,128}$/;
const UUID_V4 =
  /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const COMPACT_JWE =
  /^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/;
// The transport context refuses control characters, as the store does.
// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f]/u;

export type WalletTransportErrorCode =
  | "capture_key_invalid"
  | "capture_key_untrusted"
  | "owner_subject_invalid"
  | "value_empty"
  | "value_too_large"
  | "transport_invalid"
  | "reveal_response_invalid"
  | "reveal_context_mismatch";

/** A transport refusal. Carries a code only, never a value or ciphertext. */
export class WalletTransportClientError extends Error {
  constructor(readonly code: WalletTransportErrorCode) {
    super(code);
    this.name = "WalletTransportClientError";
  }
}

export type PublicP256Jwk = P256PublicJwk;

/** `GET /v1/wallet/capture-key` */
export interface CaptureKey {
  format: string;
  kid: string;
  keyGeneration: string;
  publicKey: PublicP256Jwk;
  thumbprint: string;
  audience: string;
  custodyNamespace: string;
  vaultRef: string;
}

export interface TransportContext {
  custodyNamespace: string;
  ownerSubject: string;
  vaultRef: string;
  itemVersion: string;
  keyGeneration: string;
  operation: string;
  operationId: string;
  audience: string;
  recipientKeyThumbprint: string;
  expiresAt: number;
}

export interface CaptureEnvelope {
  jwe: string;
  expiresAt: number;
  /** Bound into the JWE; the request must send the same operationId. */
  operationId: string;
}

export interface RevealRecipient {
  readonly privateKey: CryptoKey;
  readonly publicJwk: PublicP256Jwk;
  readonly thumbprint: string;
}

export interface RevealResponse {
  operationId?: unknown;
  itemId?: unknown;
  version?: unknown;
  jwe?: unknown;
  context?: unknown;
}

const fail = (code: WalletTransportErrorCode): never => {
  throw new WalletTransportClientError(code);
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function exactRecord(
  value: unknown,
  fields: readonly string[],
): value is Record<string, unknown> {
  return (
    isRecord(value) &&
    Object.keys(value).length === fields.length &&
    fields.every((field) => Object.hasOwn(value, field))
  );
}

function boundedString(value: unknown, max = 512): value is string {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.length <= max &&
    !CONTROL.test(value)
  );
}

/**
 * The fixed transport context as JSON with its fields in protocol order.
 * Mirrors `contextBytes` in the store; a context it would refuse is refused
 * here first.
 */
export function contextJson(context: unknown): string {
  if (!exactRecord(context, CONTEXT_FIELDS)) return fail("transport_invalid");
  const ordered: Record<string, string | number> = {};
  for (const field of CONTEXT_FIELDS) {
    const value = context[field];
    if (field === "expiresAt") {
      if (!Number.isSafeInteger(value)) return fail("transport_invalid");
    } else if (!boundedString(value)) {
      return fail("transport_invalid");
    }
    ordered[field] = value as string | number;
  }
  if (
    !OPERATIONS.has(ordered.operation as string) ||
    !DECIMAL.test(ordered.itemVersion as string) ||
    !DECIMAL.test(ordered.keyGeneration as string) ||
    !THUMBPRINT.test(ordered.recipientKeyThumbprint as string)
  )
    return fail("transport_invalid");
  return JSON.stringify(ordered);
}

/** base64url of the UTF-8 context JSON, as the protected `wallet_ctx`. */
const walletContextHeader = (json: string) =>
  base64urlEncode(new TextEncoder().encode(json));

/** A fresh UUID v4 for one Wallet operation (the lease `jti`). */
export function newOperationId(): string {
  const id = crypto.randomUUID();
  return UUID_V4.test(id) ? id : fail("transport_invalid");
}

/**
 * Milliseconds the server clock runs ahead of this browser, from a response
 * `Date` header. Envelope lifetimes are checked on the server clock; a skew
 * under 30 s is within the margin and ignored.
 */
export function clockOffsetFrom(
  response: Pick<Response, "headers">,
  now = Date.now(),
): number {
  const header = response.headers.get("date");
  const server = header ? Date.parse(header) : Number.NaN;
  if (!Number.isFinite(server)) return 0;
  const offset = server - now;
  if (Math.abs(offset) < 30_000 || Math.abs(offset) > 86_400_000) return 0;
  return offset;
}

function publicJwk(value: unknown): PublicP256Jwk {
  if (
    !exactRecord(value, ["kty", "crv", "x", "y"]) ||
    value.kty !== "EC" ||
    value.crv !== "P-256" ||
    typeof value.x !== "string" ||
    !COORDINATE.test(value.x) ||
    typeof value.y !== "string" ||
    !COORDINATE.test(value.y)
  )
    return fail("capture_key_invalid");
  return { kty: "EC", crv: "P-256", x: value.x, y: value.y };
}

/**
 * Validate a capture-key response and prove that its advertised thumbprint is
 * the RFC 7638 thumbprint of the public key it carries.
 */
export async function verifyCaptureKey(value: unknown): Promise<CaptureKey> {
  if (
    !isRecord(value) ||
    value.format !== CAPTURE_KEY_FORMAT ||
    typeof value.kid !== "string" ||
    !KID.test(value.kid) ||
    typeof value.keyGeneration !== "string" ||
    !DECIMAL.test(value.keyGeneration) ||
    typeof value.thumbprint !== "string" ||
    !THUMBPRINT.test(value.thumbprint) ||
    !boundedString(value.audience) ||
    !boundedString(value.custodyNamespace) ||
    !boundedString(value.vaultRef)
  )
    return fail("capture_key_invalid");
  const jwk = publicJwk(value.publicKey);
  if ((await p256Thumbprint(jwk)) !== value.thumbprint)
    return fail("capture_key_untrusted");
  return {
    format: value.format,
    kid: value.kid,
    keyGeneration: value.keyGeneration,
    publicKey: jwk,
    thumbprint: value.thumbprint,
    audience: value.audience,
    custodyNamespace: value.custodyNamespace,
    vaultRef: value.vaultRef,
  };
}

export interface EncryptCaptureInput {
  captureKey: CaptureKey;
  ownerSubject: string;
  /** "1" for a new item, current version + 1 for a new version. */
  itemVersion: string;
  value: string;
  operationId?: string;
  /** Server-clock milliseconds (Date.now() + clockOffsetFrom(...)). */
  now?: number;
}

/**
 * Encrypt one captured value to the store's capture key. The context binds
 * the owner, item version, key generation and this operation id.
 */
export async function encryptCapture(
  input: EncryptCaptureInput,
): Promise<CaptureEnvelope> {
  const captureKey = await verifyCaptureKey(input.captureKey);
  if (!boundedString(input.ownerSubject)) fail("owner_subject_invalid");
  if (!DECIMAL.test(input.itemVersion)) fail("transport_invalid");
  const operationId = input.operationId ?? newOperationId();
  if (!UUID_V4.test(operationId)) fail("transport_invalid");
  if (typeof input.value !== "string" || input.value.length === 0)
    fail("value_empty");

  const plaintext = new Uint8Array(new TextEncoder().encode(input.value));
  try {
    if (plaintext.byteLength > MAX_VALUE_BYTES) fail("value_too_large");
    const expiresAt = (input.now ?? Date.now()) + ENVELOPE_LIFETIME_MS;
    const context: TransportContext = {
      custodyNamespace: captureKey.custodyNamespace,
      ownerSubject: input.ownerSubject,
      vaultRef: captureKey.vaultRef,
      itemVersion: input.itemVersion,
      keyGeneration: captureKey.keyGeneration,
      operation: "capture",
      operationId,
      audience: captureKey.audience,
      recipientKeyThumbprint: captureKey.thumbprint,
      expiresAt,
    };
    const jwe = await encryptCompact(plaintext, captureKey.publicKey, {
      typ: TRANSPORT_TYPE,
      kid: captureKey.kid,
      crit: ["wallet_ctx"],
      wallet_ctx: walletContextHeader(contextJson(context)),
    });
    return { jwe, expiresAt, operationId };
  } catch (error) {
    if (error instanceof WalletTransportClientError) throw error;
    return fail("transport_invalid");
  } finally {
    plaintext.fill(0);
  }
}

/**
 * An ephemeral P-256 ECDH key pair for one reveal. The private half is not
 * extractable and lives only in this browser tab's memory.
 */
export async function createRevealRecipient(): Promise<RevealRecipient> {
  const pair = (await crypto.subtle.generateKey(
    { name: "ECDH", namedCurve: "P-256" },
    false,
    ["deriveBits"],
  )) as CryptoKeyPair;
  const exported = await crypto.subtle.exportKey("jwk", pair.publicKey);
  const jwk = publicJwk({
    kty: exported.kty,
    crv: exported.crv,
    x: exported.x,
    y: exported.y,
  });
  return Object.freeze({
    privateKey: pair.privateKey,
    publicJwk: jwk,
    thumbprint: await p256Thumbprint(jwk),
  });
}

export interface DecryptRevealInput {
  recipient: RevealRecipient;
  response: RevealResponse;
  ownerSubject: string;
  itemId: string;
  /** The expiresAt this browser sent with the reveal request. */
  expiresAt: number;
  /** Server-clock milliseconds (Date.now() + clockOffsetFrom(...)). */
  now?: number;
}

/**
 * Decrypt a revealed value after checking that the protected `wallet_ctx`
 * equals the returned context, names a reveal of the returned version to this
 * owner and this recipient key, and has not expired.
 */
export async function decryptReveal(
  input: DecryptRevealInput,
): Promise<string> {
  const { recipient, response } = input;
  if (
    !isRecord(response) ||
    typeof response.jwe !== "string" ||
    response.jwe.length > MAX_ENVELOPE_BYTES ||
    !COMPACT_JWE.test(response.jwe) ||
    typeof response.version !== "string" ||
    !DECIMAL.test(response.version) ||
    response.itemId !== input.itemId
  )
    return fail("reveal_response_invalid");

  let expected: string;
  try {
    expected = contextJson(response.context);
  } catch {
    return fail("reveal_response_invalid");
  }
  const context = JSON.parse(expected) as TransportContext;
  const now = input.now ?? Date.now();
  if (
    context.operation !== "reveal" ||
    context.ownerSubject !== input.ownerSubject ||
    context.itemVersion !== response.version ||
    context.audience !== REVEAL_AUDIENCE ||
    context.recipientKeyThumbprint !== recipient.thumbprint ||
    context.expiresAt !== input.expiresAt ||
    (response.operationId !== undefined &&
      context.operationId !== response.operationId) ||
    context.expiresAt <= now ||
    context.expiresAt > now + MAX_LIFETIME_MS
  )
    return fail("reveal_context_mismatch");

  // The protected header is public, so it is checked before any key operation:
  // `zip` (compression), an unapproved parameter or a downgraded algorithm is
  // refused before a key is derived, anything is decrypted or inflated. It is
  // checked again on what was opened.
  const checkHeader = (header: Record<string, unknown>): void => {
    const epk = header.epk as Record<string, unknown> | undefined;
    if (
      Object.keys(header).some((field) => !HEADER_FIELDS.has(field)) ||
      header.alg !== KEY_MANAGEMENT ||
      header.enc !== CONTENT_ENCRYPTION ||
      header.typ !== TRANSPORT_TYPE ||
      header.kid !== REVEAL_RECIPIENT_KID ||
      !Array.isArray(header.crit) ||
      header.crit.length !== 1 ||
      header.crit[0] !== "wallet_ctx" ||
      header.wallet_ctx !== walletContextHeader(expected) ||
      epk?.kty !== "EC" ||
      epk?.crv !== "P-256"
    )
      return fail("reveal_context_mismatch");
  };

  let plaintext: Uint8Array | undefined;
  try {
    checkHeader(decodeProtectedHeader(response.jwe));
    const opened = await decryptCompact(response.jwe, recipient.privateKey);
    plaintext = opened.plaintext;
    checkHeader(opened.header);
    if (plaintext.byteLength === 0 || plaintext.byteLength > MAX_VALUE_BYTES)
      return fail("reveal_response_invalid");
    return new TextDecoder("utf-8", { fatal: true }).decode(plaintext);
  } catch (error) {
    if (error instanceof WalletTransportClientError) throw error;
    return fail("reveal_response_invalid");
  } finally {
    plaintext?.fill(0);
  }
}
