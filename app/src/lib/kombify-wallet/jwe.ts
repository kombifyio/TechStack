/**
 * The one JWE profile of the kombify Wallet transport, on WebCrypto only:
 * compact serialization, `ECDH-ES+A256KW` key management and `A256GCM` content
 * encryption over P-256 (RFC 7516, RFC 7518 sections 4.6 and 5.3).
 *
 * This is deliberately not a general JOSE library. It opens and seals exactly
 * the envelopes `kombify.wallet-transport.v1` allows and refuses everything
 * else; the caller (transport.ts) adds the Wallet's context rules on top.
 * Nothing here logs or stores a plaintext, a key or a ciphertext.
 */

export const KEY_MANAGEMENT = "ECDH-ES+A256KW";
export const CONTENT_ENCRYPTION = "A256GCM";

const CEK_BYTES = 32;
const WRAPPED_CEK_BYTES = 40;
const IV_BYTES = 12;
const TAG_BYTES = 16;
const COORDINATE = /^[A-Za-z0-9_-]{43}$/;

export interface P256PublicJwk {
  kty: "EC";
  crv: "P-256";
  x: string;
  y: string;
}

const textEncoder = new TextEncoder();

/** A failure to seal or open an envelope. Carries no key or value material. */
export class JweError extends Error {
  constructor() {
    super("jwe_invalid");
    this.name = "JweError";
  }
}

const fail = (): never => {
  throw new JweError();
};

export function base64urlEncode(bytes: Uint8Array): string {
  let binary = "";
  for (let index = 0; index < bytes.length; index += 0x8000)
    binary += String.fromCharCode(...bytes.subarray(index, index + 0x8000));
  return btoa(binary)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

export function base64urlDecode(text: string): Uint8Array<ArrayBuffer> {
  if (!/^[A-Za-z0-9_-]*$/.test(text) || text.length % 4 === 1) return fail();
  const padded = text.replace(/-/g, "+").replace(/_/g, "/");
  const binary = atob(padded + "=".repeat((4 - (padded.length % 4)) % 4));
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1)
    bytes[index] = binary.charCodeAt(index);
  return bytes;
}

function uint32(value: number): Uint8Array<ArrayBuffer> {
  const bytes = new Uint8Array(4);
  new DataView(bytes.buffer).setUint32(0, value, false);
  return bytes;
}

function concat(...parts: Uint8Array[]): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(parts.reduce((sum, part) => sum + part.length, 0));
  let offset = 0;
  for (const part of parts) {
    out.set(part, offset);
    offset += part.length;
  }
  return out;
}

const lengthPrefixed = (bytes: Uint8Array) =>
  concat(uint32(bytes.length), bytes);

/**
 * Concat KDF of NIST SP 800-56A as profiled by RFC 7518 section 4.6.2, with
 * SHA-256. `algorithmId` is the `enc` value for direct agreement and the `alg`
 * value for key wrapping. `keyBits` up to 256 needs a single hash round, which
 * is all this profile ever asks for.
 */
export async function concatKdf(
  sharedSecret: Uint8Array,
  algorithmId: string,
  keyBits: number,
  partyUInfo: Uint8Array = new Uint8Array(0),
  partyVInfo: Uint8Array = new Uint8Array(0),
): Promise<Uint8Array<ArrayBuffer>> {
  if (keyBits <= 0 || keyBits > 256 || keyBits % 8 !== 0) return fail();
  const input = concat(
    uint32(1),
    sharedSecret,
    lengthPrefixed(textEncoder.encode(algorithmId)),
    lengthPrefixed(partyUInfo),
    lengthPrefixed(partyVInfo),
    uint32(keyBits),
  );
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", input));
  input.fill(0);
  return digest.slice(0, keyBits / 8);
}

/** RFC 7638 thumbprint of a P-256 public key: base64url SHA-256 of its canonical JSON. */
export async function p256Thumbprint(jwk: P256PublicJwk): Promise<string> {
  if (
    jwk.kty !== "EC" ||
    jwk.crv !== "P-256" ||
    !COORDINATE.test(jwk.x) ||
    !COORDINATE.test(jwk.y)
  )
    return fail();
  const canonical = JSON.stringify({
    crv: jwk.crv,
    kty: jwk.kty,
    x: jwk.x,
    y: jwk.y,
  });
  return base64urlEncode(
    new Uint8Array(
      await crypto.subtle.digest("SHA-256", textEncoder.encode(canonical)),
    ),
  );
}

/** The protected header of a compact JWE, parsed before any key is used. */
export function decodeProtectedHeader(
  compact: string,
): Record<string, unknown> {
  const [encoded] = compact.split(".");
  let parsed: unknown;
  try {
    parsed = JSON.parse(
      new TextDecoder("utf-8", { fatal: true }).decode(
        base64urlDecode(encoded ?? ""),
      ),
    );
  } catch {
    return fail();
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed))
    return fail();
  return parsed as Record<string, unknown>;
}

async function importPublic(jwk: P256PublicJwk): Promise<CryptoKey> {
  return crypto.subtle.importKey(
    "jwk",
    { kty: jwk.kty, crv: jwk.crv, x: jwk.x, y: jwk.y, ext: true },
    { name: "ECDH", namedCurve: "P-256" },
    true,
    [],
  );
}

async function wrappingKey(
  privateKey: CryptoKey,
  publicKey: CryptoKey,
  usages: KeyUsage[],
): Promise<CryptoKey> {
  const secret = new Uint8Array(
    await crypto.subtle.deriveBits(
      { name: "ECDH", public: publicKey },
      privateKey,
      256,
    ),
  );
  try {
    const kek = await concatKdf(secret, KEY_MANAGEMENT, 256);
    try {
      return await crypto.subtle.importKey("raw", kek, "AES-KW", false, usages);
    } finally {
      kek.fill(0);
    }
  } finally {
    secret.fill(0);
  }
}

/**
 * Seal `plaintext` to a P-256 public key. `header` is the application's
 * protected header; `alg`, `enc` and the ephemeral `epk` are set here, so a
 * caller cannot pick a different algorithm.
 */
export async function encryptCompact(
  plaintext: Uint8Array,
  recipient: P256PublicJwk,
  header: Record<string, unknown>,
): Promise<string> {
  const recipientKey = await importPublic(recipient);
  const ephemeral = (await crypto.subtle.generateKey(
    { name: "ECDH", namedCurve: "P-256" },
    true,
    ["deriveBits"],
  )) as CryptoKeyPair;
  const epk = await crypto.subtle.exportKey("jwk", ephemeral.publicKey);
  const protectedHeader = {
    ...header,
    alg: KEY_MANAGEMENT,
    enc: CONTENT_ENCRYPTION,
    epk: { kty: epk.kty, crv: epk.crv, x: epk.x, y: epk.y },
  };
  const encodedHeader = base64urlEncode(
    textEncoder.encode(JSON.stringify(protectedHeader)),
  );
  const kek = await wrappingKey(ephemeral.privateKey, recipientKey, [
    "wrapKey",
  ]);
  const cekBytes = crypto.getRandomValues(new Uint8Array(CEK_BYTES));
  try {
    const cek = await crypto.subtle.importKey(
      "raw",
      cekBytes,
      "AES-GCM",
      true,
      ["encrypt"],
    );
    const wrapped = new Uint8Array(
      await crypto.subtle.wrapKey("raw", cek, kek, "AES-KW"),
    );
    const iv = crypto.getRandomValues(new Uint8Array(IV_BYTES));
    const sealed = new Uint8Array(
      await crypto.subtle.encrypt(
        {
          name: "AES-GCM",
          iv,
          additionalData: textEncoder.encode(encodedHeader),
          tagLength: TAG_BYTES * 8,
        },
        cek,
        plaintext as Uint8Array<ArrayBuffer>,
      ),
    );
    const cut = sealed.length - TAG_BYTES;
    return [
      encodedHeader,
      base64urlEncode(wrapped),
      base64urlEncode(iv),
      base64urlEncode(sealed.subarray(0, cut)),
      base64urlEncode(sealed.subarray(cut)),
    ].join(".");
  } finally {
    cekBytes.fill(0);
  }
}

/**
 * Open a compact JWE with the recipient's ECDH private key. The caller checks
 * the protected header first (`decodeProtectedHeader`); this still refuses an
 * envelope that is not exactly this profile. The returned bytes belong to the
 * caller, who wipes them.
 */
export async function decryptCompact(
  compact: string,
  recipientPrivateKey: CryptoKey,
): Promise<{
  plaintext: Uint8Array<ArrayBuffer>;
  header: Record<string, unknown>;
}> {
  const parts = compact.split(".");
  if (parts.length !== 5) return fail();
  const [encodedHeader, encodedKey, encodedIv, encodedText, encodedTag] = parts;
  const header = decodeProtectedHeader(compact);
  const epk = header.epk as Record<string, unknown> | undefined;
  if (
    header.alg !== KEY_MANAGEMENT ||
    header.enc !== CONTENT_ENCRYPTION ||
    header.zip !== undefined ||
    !epk ||
    epk.kty !== "EC" ||
    epk.crv !== "P-256" ||
    typeof epk.x !== "string" ||
    typeof epk.y !== "string"
  )
    return fail();
  const wrapped = base64urlDecode(encodedKey);
  const iv = base64urlDecode(encodedIv);
  const text = base64urlDecode(encodedText);
  const tag = base64urlDecode(encodedTag);
  if (
    wrapped.length !== WRAPPED_CEK_BYTES ||
    iv.length !== IV_BYTES ||
    tag.length !== TAG_BYTES
  )
    return fail();
  const sender = await importPublic({
    kty: "EC",
    crv: "P-256",
    x: epk.x,
    y: epk.y,
  });
  const kek = await wrappingKey(recipientPrivateKey, sender, ["unwrapKey"]);
  const cek = await crypto.subtle.unwrapKey(
    "raw",
    wrapped,
    kek,
    "AES-KW",
    { name: "AES-GCM" },
    false,
    ["decrypt"],
  );
  const plaintext = new Uint8Array(
    await crypto.subtle.decrypt(
      {
        name: "AES-GCM",
        iv,
        additionalData: textEncoder.encode(encodedHeader),
        tagLength: TAG_BYTES * 8,
      },
      cek,
      concat(text, tag),
    ),
  );
  return { plaintext, header };
}
