import { describe, expect, it } from "vitest";
import {
  REVEAL_AUDIENCE,
  WalletTransportClientError,
  createRevealRecipient,
  decryptReveal,
  encryptCapture,
  verifyCaptureKey,
  type CaptureKey,
  type RevealRecipient,
  type TransportContext,
} from "./transport";
import { concatKdf, base64urlEncode } from "./jwe";
import { createStoreSimulator } from "../testing/wallet-store-simulator";

const OWNER = "auth0|wallet-owner-fixture";
const ITEM = "itm_abcdefghijklmnopqrstuvwxyz";

async function reveal(value: string) {
  const store = createStoreSimulator();
  const recipient = await createRevealRecipient();
  const expiresAt = Date.now() + 120_000;
  const context: TransportContext = {
    custodyNamespace: "personal",
    ownerSubject: OWNER,
    vaultRef: "personal",
    itemVersion: "2",
    keyGeneration: "1",
    operation: "reveal",
    operationId: crypto.randomUUID(),
    audience: REVEAL_AUDIENCE,
    recipientKeyThumbprint: recipient.thumbprint,
    expiresAt,
  };
  const jwe = store.sealReveal(value, recipient.publicJwk, context);
  const response = {
    operationId: context.operationId,
    itemId: ITEM,
    version: "2",
    jwe,
    context,
  };
  return { store, recipient, expiresAt, context, response };
}

function open(
  fixture: Awaited<ReturnType<typeof reveal>>,
  change: {
    response?: Record<string, unknown>;
    ownerSubject?: string;
    recipient?: RevealRecipient;
  } = {},
) {
  return decryptReveal({
    recipient: change.recipient ?? fixture.recipient,
    response: change.response ?? fixture.response,
    ownerSubject: change.ownerSubject ?? OWNER,
    itemId: ITEM,
    expiresAt: fixture.expiresAt,
  });
}

const refusal = (promise: Promise<unknown>) =>
  promise.then(
    () => undefined,
    (error: unknown) =>
      error instanceof WalletTransportClientError ? error.code : String(error),
  );

describe("wallet transport (browser side)", () => {
  it("seals a captured value only the store can open, bound to the owner, version and operation", async () => {
    const store = createStoreSimulator({ kid: "wallet-capture-2026" });
    const captureKey = await verifyCaptureKey(store.captureKey);
    const value = JSON.stringify({ username: "ada", password: "s3cr3t-✓" });
    const envelope = await encryptCapture({
      captureKey,
      ownerSubject: OWNER,
      itemVersion: "4",
      value,
    });
    const expected = {
      custodyNamespace: captureKey.custodyNamespace,
      ownerSubject: OWNER,
      vaultRef: captureKey.vaultRef,
      itemVersion: "4",
      keyGeneration: captureKey.keyGeneration,
      operation: "capture",
      operationId: envelope.operationId,
      audience: captureKey.audience,
      recipientKeyThumbprint: captureKey.thumbprint,
      expiresAt: envelope.expiresAt,
    };
    expect(store.openCapture(envelope.jwe, expected)).toBe(value);
    for (const change of [
      { ownerSubject: "auth0|someone-else" },
      { itemVersion: "5" },
      { operationId: crypto.randomUUID() },
    ])
      expect(() =>
        store.openCapture(envelope.jwe, { ...expected, ...change }),
      ).toThrow();
    expect(envelope.jwe.includes("s3cr3t")).toBe(false);
  });

  it("refuses to encrypt to a capture key whose thumbprint is not its own", async () => {
    const real = createStoreSimulator().captureKey as unknown as CaptureKey;
    const other = createStoreSimulator().captureKey as unknown as CaptureKey;
    const outcome = await refusal(
      encryptCapture({
        captureKey: { ...real, thumbprint: other.thumbprint },
        ownerSubject: OWNER,
        itemVersion: "1",
        value: "token-value",
      }),
    );
    expect(outcome).toBe("capture_key_untrusted");
  });

  it("reveals a value the store sealed to this browser's ephemeral key", async () => {
    const fixture = await reveal("ghp_revealed-token");
    await expect(open(fixture)).resolves.toBe("ghp_revealed-token");
  });

  it("refuses a reveal whose context does not match the header, the owner, the operation or the version", async () => {
    const fixture = await reveal("never-shown");
    const attempts: Parameters<typeof open>[1][] = [
      // A reveal sealed to another browser's key.
      { recipient: await createRevealRecipient() },
      {
        response: {
          ...fixture.response,
          context: { ...fixture.context, vaultRef: "shared" },
        },
      },
      { ownerSubject: "auth0|someone-else" },
      {
        response: {
          ...fixture.response,
          context: { ...fixture.context, operation: "capture" },
        },
      },
      { response: { ...fixture.response, version: "3" } },
    ];
    for (const attempt of attempts)
      expect(await refusal(open(fixture, attempt))).toBe(
        "reveal_context_mismatch",
      );
  });

  it("refuses a compressed reveal on its header before decrypting, whoever it is sealed to", async () => {
    const fixture = await reveal("never-shown");
    const stranger = await createRevealRecipient();
    for (const sealedTo of [fixture.recipient, stranger]) {
      const jwe = fixture.store.sealReveal(
        "never-shown",
        sealedTo.publicJwk,
        fixture.context,
        { zip: "DEF" },
      );
      // A header refusal never depends on the key: an envelope this browser
      // could not even open is refused for its header, not as a failed open.
      expect(
        await refusal(
          open(fixture, { response: { ...fixture.response, jwe } }),
        ),
      ).toBe("reveal_context_mismatch");
    }
    await expect(open(fixture)).resolves.toBe("never-shown");
  });

  it("opens a reveal sealed by the Gateway's Wallet Store (jose transport vector)", async () => {
    // Produced once with Gateway services/wallet-store/src/transport.mjs
    // `encryptTransport` (jose); the private key exists only for this vector.
    const vector = {
      recipientPrivateJwk: {
        kty: "EC",
        crv: "P-256",
        x: "GdkEXOztpcajXgxIxw9RYfnaUTJawPuPikmlwslSkvU",
        y: "6q-o4XbJFb9STxUzrcNciUvMbXCktWomRTUxpTYGwXE",
        d: "1zRmVWz1OT3hwIs56K-2u04l-fL-mRCJTB9I4EUlR4E",
      },
      thumbprint: "Vn1pHxrMzu96_1EgpAYrJ4eEG_vDn7AEo3Hi4GafaLM",
      context: {
        custodyNamespace: "personal",
        ownerSubject: "auth0|techstack-vector-owner",
        vaultRef: "personal",
        itemVersion: "3",
        keyGeneration: "1",
        operation: "reveal",
        operationId: "0f8fad5b-d9cb-469f-a165-70867728950e",
        audience: "urn:kombify:wallet:owner-session",
        recipientKeyThumbprint: "Vn1pHxrMzu96_1EgpAYrJ4eEG_vDn7AEo3Hi4GafaLM",
        expiresAt: 1791653608312,
      },
      value:
        '{"host":"vector.example","user":"root","privateKey":"vector-not-a-real-key"}',
      jwe: "eyJhbGciOiJFQ0RILUVTK0EyNTZLVyIsImVuYyI6IkEyNTZHQ00iLCJ0eXAiOiJrb21iaWZ5LndhbGxldC10cmFuc3BvcnQudjEiLCJraWQiOiJvd25lci1zZXNzaW9uIiwiY3JpdCI6WyJ3YWxsZXRfY3R4Il0sIndhbGxldF9jdHgiOiJleUpqZFhOMGIyUjVUbUZ0WlhOd1lXTmxJam9pY0dWeWMyOXVZV3dpTENKdmQyNWxjbE4xWW1wbFkzUWlPaUpoZFhSb01IeDBaV05vYzNSaFkyc3RkbVZqZEc5eUxXOTNibVZ5SWl3aWRtRjFiSFJTWldZaU9pSndaWEp6YjI1aGJDSXNJbWwwWlcxV1pYSnphVzl1SWpvaU15SXNJbXRsZVVkbGJtVnlZWFJwYjI0aU9pSXhJaXdpYjNCbGNtRjBhVzl1SWpvaWNtVjJaV0ZzSWl3aWIzQmxjbUYwYVc5dVNXUWlPaUl3WmpobVlXUTFZaTFrT1dOaUxUUTJPV1l0WVRFMk5TMDNNRGcyTnpjeU9EazFNR1VpTENKaGRXUnBaVzVqWlNJNkluVnlianByYjIxaWFXWjVPbmRoYkd4bGREcHZkMjVsY2kxelpYTnphVzl1SWl3aWNtVmphWEJwWlc1MFMyVjVWR2gxYldKd2NtbHVkQ0k2SWxadU1YQkllSEpOZW5VNU5sOHhSV2R3UVZseVNqUmxSVWRmZGtSdU4wRkZiek5JYVRSSFlXWmhURTBpTENKbGVIQnBjbVZ6UVhRaU9qRTNPVEUyTlRNMk1EZ3pNVEo5IiwiZXBrIjp7IngiOiJDeGMxREt0enEwUmlsWHhIWU5XdHVqRGJoRjNYVTF0TGU4OXViYkJsdEZnIiwiY3J2IjoiUC0yNTYiLCJrdHkiOiJFQyIsInkiOiJvSWp4X29jZGJlTlBuZU0zNVdIdnY5Y3dhZjhDcE5TczRCSVhkMllReHlvIn19.d6elmMzMpjcRfRfTQhR3ulUOLDIcbpCR9tcGAsXtTC3ahtOj_nc_gg.Z7_zZS-A1Sd85KPb.aWSjnJ4XBw_IuMsBgUZdZ76Am_qrcm6lDh1jJfN9R5JJi9c1epeRMCxy5DgJMyEoDxGsx1oOBjIN-cTANp-Sz9niE7rV9Q_91f808g.9tuifOQMyWY12TG4UUyRpA",
    };
    const { d: _private, ...publicJwk } = vector.recipientPrivateJwk;
    const privateKey = await crypto.subtle.importKey(
      "jwk",
      vector.recipientPrivateJwk,
      { name: "ECDH", namedCurve: "P-256" },
      false,
      ["deriveBits"],
    );
    const recipient: RevealRecipient = {
      privateKey,
      publicJwk: publicJwk as RevealRecipient["publicJwk"],
      thumbprint: vector.thumbprint,
    };
    const value = await decryptReveal({
      recipient,
      response: {
        itemId: ITEM,
        version: "3",
        operationId: vector.context.operationId,
        jwe: vector.jwe,
        context: vector.context,
      },
      ownerSubject: vector.context.ownerSubject,
      itemId: ITEM,
      expiresAt: vector.context.expiresAt,
      now: vector.context.expiresAt - 60_000,
    });
    expect(value).toBe(vector.value);
  });

  it("pins the RFC 7517 appendix A thumbprint and the RFC 7518 appendix C key derivation", async () => {
    const key = await verifyCaptureKey({
      format: "kombify.wallet-capture-key.v1",
      kid: "rfc7517-a",
      keyGeneration: "1",
      publicKey: {
        kty: "EC",
        crv: "P-256",
        x: "MKBCTNIcKUSDii11ySs3526iDZ8AiTo7Tu6KPAqv7D4",
        y: "4Etl6SRW2YiLUrN5vfvVHuhp7x8PxltmWWlbbM4IFyM",
      },
      thumbprint: "cn-I_WNMClehiVp51i_0VpOENW1upEerA8sEam5hn-s",
      audience: "urn:kombify:wallet-store",
      custodyNamespace: "personal",
      vaultRef: "personal",
    });
    expect(key.thumbprint).toBe("cn-I_WNMClehiVp51i_0VpOENW1upEerA8sEam5hn-s");

    // RFC 7518 appendix C: ECDH-ES with A128GCM, apu "Alice", apv "Bob".
    const shared = Uint8Array.from([
      158, 86, 217, 29, 129, 113, 53, 211, 114, 131, 66, 131, 191, 132, 38, 156,
      251, 49, 110, 163, 218, 128, 106, 72, 246, 218, 167, 121, 140, 254, 144,
      196,
    ]);
    const derived = await concatKdf(
      shared,
      "A128GCM",
      128,
      new TextEncoder().encode("Alice"),
      new TextEncoder().encode("Bob"),
    );
    expect(base64urlEncode(derived)).toBe("VqqN6vgjbSBcIijNcacQGg");
  });
});
