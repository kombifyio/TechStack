/**
 * Test-only stand-in for the Wallet Store side of `kombify.wallet-transport.v1`
 * (Gateway `services/wallet-store/src/transport.mjs`), written from RFC 7516
 * and RFC 7518 on `node:crypto`, so it shares no code with the browser
 * implementation in `kombify-wallet/jwe.ts` (WebCrypto). It opens captured
 * values the way `decryptTransport` does and seals revealed values the way
 * `encryptTransport` does: a header or context that differs from the one the
 * store builds itself is refused.
 *
 * Never imported by application code.
 */
import {
  createCipheriv,
  createDecipheriv,
  createHash,
  createPublicKey,
  diffieHellman,
  generateKeyPairSync,
  randomBytes,
  type JsonWebKey,
  type KeyObject,
} from "node:crypto";

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
const ALG = "ECDH-ES+A256KW";
const ENC = "A256GCM";
const TYP = "kombify.wallet-transport.v1";
const AES_KW_IV = Buffer.from("A6A6A6A6A6A6A6A6", "hex");

export interface SimulatedContext {
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

export interface PublicJwk {
  kty: "EC";
  crv: "P-256";
  x: string;
  y: string;
}

const b64u = (bytes: Uint8Array) => Buffer.from(bytes).toString("base64url");

export function thumbprint(jwk: PublicJwk): string {
  return createHash("sha256")
    .update(JSON.stringify({ crv: jwk.crv, kty: jwk.kty, x: jwk.x, y: jwk.y }))
    .digest("base64url");
}

function contextHeader(context: SimulatedContext): string {
  const ordered = Object.fromEntries(
    CONTEXT_FIELDS.map((field) => [field, context[field]]),
  );
  return b64u(Buffer.from(JSON.stringify(ordered)));
}

function uint32(value: number): Buffer {
  const buffer = Buffer.alloc(4);
  buffer.writeUInt32BE(value);
  return buffer;
}

/** RFC 7518 4.6.2 with SHA-256; one round covers the 256 bits asked for. */
function kek(sharedSecret: Buffer): Buffer {
  const algorithm = Buffer.from(ALG);
  return createHash("sha256")
    .update(uint32(1))
    .update(sharedSecret)
    .update(uint32(algorithm.length))
    .update(algorithm)
    .update(uint32(0))
    .update(uint32(0))
    .update(uint32(256))
    .digest();
}

function publicKeyOf(jwk: PublicJwk): KeyObject {
  return createPublicKey({ key: jwk as JsonWebKey, format: "jwk" });
}

export interface StoreSimulator {
  /** The body of `GET /v1/wallet/capture-key`. */
  captureKey: Record<string, unknown>;
  /** Open a captured JWE against the context the store expects. */
  openCapture(jwe: string, expected: SimulatedContext): string;
  /** Seal a revealed value to the owner's ephemeral key. */
  sealReveal(
    value: string,
    recipient: PublicJwk,
    context: SimulatedContext,
    extraHeader?: Record<string, unknown>,
  ): string;
}

export function createStoreSimulator(
  options: {
    kid?: string;
    keyGeneration?: string;
    audience?: string;
    custodyNamespace?: string;
    vaultRef?: string;
  } = {},
): StoreSimulator {
  const kid = options.kid ?? "wallet-capture-sim";
  const pair = generateKeyPairSync("ec", { namedCurve: "P-256" });
  const jwk = pair.publicKey.export({ format: "jwk" });
  const publicKey: PublicJwk = {
    kty: "EC",
    crv: "P-256",
    x: jwk.x as string,
    y: jwk.y as string,
  };
  const captureKey = {
    format: "kombify.wallet-capture-key.v1",
    kid,
    keyGeneration: options.keyGeneration ?? "1",
    publicKey,
    thumbprint: thumbprint(publicKey),
    audience: options.audience ?? "urn:kombify:wallet-store",
    custodyNamespace: options.custodyNamespace ?? "personal",
    vaultRef: options.vaultRef ?? "personal",
  };

  return {
    captureKey,
    openCapture(jwe, expected) {
      const parts = jwe.split(".");
      if (parts.length !== 5) throw new Error("wallet_transport_invalid");
      const [encodedHeader, encodedKey, encodedIv, encodedText, encodedTag] =
        parts;
      const header = JSON.parse(
        Buffer.from(encodedHeader, "base64url").toString("utf8"),
      ) as Record<string, unknown>;
      const epk = header.epk as Record<string, unknown> | undefined;
      const now = Date.now();
      if (
        Object.keys(header).some((field) => !HEADER_FIELDS.has(field)) ||
        header.alg !== ALG ||
        header.enc !== ENC ||
        header.typ !== TYP ||
        header.kid !== kid ||
        JSON.stringify(header.crit) !== JSON.stringify(["wallet_ctx"]) ||
        header.wallet_ctx !== contextHeader(expected) ||
        epk?.kty !== "EC" ||
        epk?.crv !== "P-256" ||
        expected.expiresAt <= now ||
        expected.expiresAt > now + 300_000
      )
        throw new Error("wallet_transport_invalid");
      const secret = diffieHellman({
        privateKey: pair.privateKey,
        publicKey: publicKeyOf(epk as unknown as PublicJwk),
      });
      const unwrap = createDecipheriv("id-aes256-wrap", kek(secret), AES_KW_IV);
      const cek = Buffer.concat([
        unwrap.update(Buffer.from(encodedKey, "base64url")),
        unwrap.final(),
      ]);
      const decipher = createDecipheriv(
        "aes-256-gcm",
        cek,
        Buffer.from(encodedIv, "base64url"),
      );
      decipher.setAAD(Buffer.from(encodedHeader, "ascii"));
      decipher.setAuthTag(Buffer.from(encodedTag, "base64url"));
      return Buffer.concat([
        decipher.update(Buffer.from(encodedText, "base64url")),
        decipher.final(),
      ]).toString("utf8");
    },

    sealReveal(value, recipient, context, extraHeader = {}) {
      const ephemeral = generateKeyPairSync("ec", { namedCurve: "P-256" });
      const epkJwk = ephemeral.publicKey.export({ format: "jwk" });
      const header = {
        alg: ALG,
        enc: ENC,
        typ: TYP,
        kid: "owner-session",
        crit: ["wallet_ctx"],
        wallet_ctx: contextHeader(context),
        epk: { kty: "EC", crv: "P-256", x: epkJwk.x, y: epkJwk.y },
        ...extraHeader,
      };
      const encodedHeader = b64u(Buffer.from(JSON.stringify(header)));
      const secret = diffieHellman({
        privateKey: ephemeral.privateKey,
        publicKey: publicKeyOf(recipient),
      });
      const cek = randomBytes(32);
      const wrap = createCipheriv("id-aes256-wrap", kek(secret), AES_KW_IV);
      const wrapped = Buffer.concat([wrap.update(cek), wrap.final()]);
      const iv = randomBytes(12);
      const cipher = createCipheriv("aes-256-gcm", cek, iv);
      cipher.setAAD(Buffer.from(encodedHeader, "ascii"));
      const text = Buffer.concat([
        cipher.update(Buffer.from(value, "utf8")),
        cipher.final(),
      ]);
      return [
        encodedHeader,
        b64u(wrapped),
        b64u(iv),
        b64u(text),
        b64u(cipher.getAuthTag()),
      ].join(".");
    },
  };
}
