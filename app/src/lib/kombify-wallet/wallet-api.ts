/**
 * Browser client of the kombify Wallet owner API at the public Gateway
 * (`https://api.kombify.io/v1/wallet/*`). The Gateway authenticates the
 * signed-in person's Auth0 access token (the same bearer every Techstack
 * gateway call carries) and derives the Wallet owner from its `sub`; Techstack
 * itself is not in the path. Every value it sends or receives is a JWE the
 * transport (`transport.ts`) produced or will open; nothing here logs or
 * stores a response body.
 */
import { resolveGatewayBearer } from "#lib/api/client.js";
import { isGatewayLoginRedirecting } from "#lib/auth/session-recovery.js";
import {
  clockOffsetFrom,
  createRevealRecipient,
  decryptReveal,
  encryptCapture,
  ENVELOPE_LIFETIME_MS,
  verifyCaptureKey,
} from "./transport.js";
import {
  creatorServicesFrom,
  serviceGrantsFrom,
  TECHSTACK_SERVICE,
  techstackMayUse,
  type ServiceRightsGrant,
  type WalletRight,
  type WalletService,
} from "./service-access.js";
import {
  certificateMatches,
  type SignPolicy,
  type SignRequest,
  type SubjectKey,
} from "./ssh-ca.js";
import type { ItemPolicy } from "./item-settings.js";

const GATEWAY_ORIGIN = (() => {
  const configured = (
    import.meta.env.VITE_GATEWAY_API_BASE as string | undefined
  )?.trim();
  if (configured) {
    try {
      return new URL(configured).origin;
    } catch {
      /* fall through to the public gateway */
    }
  }
  return "https://api.kombify.io";
})();

/** The Wallet owner API base, always the hosted Gateway. */
export const WALLET_API_BASE = `${GATEWAY_ORIGIN}/v1/wallet`;
const REQUEST_TIMEOUT_MS = 20_000;

/**
 * How far the server clock runs ahead of this browser, learned from responses
 * that expose their `Date` header. Envelope lifetimes are checked on the
 * server clock; a skew under 30 s is within the margin and ignored.
 */
let serverClockOffset = 0;
const serverNow = () => Date.now() + serverClockOffset;

export type WalletItemState = "active" | "revoked" | "deleted";

export interface WalletItem {
  itemId: string;
  itemType: string;
  displayName: string;
  scope: string;
  description?: string;
  /** Variables and configurations: the name they resolve under. */
  name?: string;
  state: WalletItemState | string;
  currentVersion: string | null;
  createdAt: number;
  updatedAt: number;
  policy?: unknown;
  useTargets?: unknown;
  mintPolicy?: unknown;
  /** SSH CAs: what the authority may sign, and the public key servers trust. */
  signPolicy?: unknown;
  caPublicKey?: string;
  /** Value releases since the last version (`{ sinceRotation, lastAt, … }`). */
  disclosure?: unknown;
}

export interface WalletOperationResult {
  operationId: string;
  itemId: string;
  version: string;
  state: string;
  auditPending: boolean;
}

/** A Wallet refusal: the Gateway denial envelope, by reason code. */
export class WalletApiError extends Error {
  constructor(
    readonly status: number,
    readonly reason: string,
    readonly retryable: boolean,
    readonly guidance: string | null,
  ) {
    super(reason);
    this.name = "WalletApiError";
  }
}

async function readJson(response: Response): Promise<Record<string, unknown>> {
  try {
    const value: unknown = await response.json();
    return value && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : {};
  } catch {
    return {};
  }
}

/** The denial's own guidance text: a string, or a `{ body }` envelope. */
function guidanceOf(body: Record<string, unknown>): string | null {
  const guidance = body.user_guidance;
  if (typeof guidance === "string") return guidance;
  if (guidance && typeof guidance === "object") {
    const text = (guidance as { body?: unknown }).body;
    if (typeof text === "string") return text;
  }
  return null;
}

async function bearer(url: string, method: string): Promise<string> {
  try {
    return await resolveGatewayBearer(url, method);
  } catch (error) {
    if (isGatewayLoginRedirecting(error)) throw error;
    throw new WalletApiError(401, "auth_required", false, null);
  }
}

async function request(
  path: string,
  init: { method?: string; body?: string } = {},
): Promise<Record<string, unknown>> {
  const method = init.method ?? "GET";
  const url = `${WALLET_API_BASE}/${path}`;
  const token = await bearer(url, method);
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  let response: Response;
  try {
    response = await fetch(url, {
      method,
      body: init.body,
      cache: "no-store",
      credentials: "omit",
      signal: controller.signal,
      headers: {
        accept: "application/json",
        authorization: `Bearer ${token}`,
        ...(init.body !== undefined
          ? { "content-type": "application/json" }
          : {}),
      },
    });
  } catch {
    throw new WalletApiError(0, "network_unavailable", true, null);
  } finally {
    clearTimeout(timeout);
  }
  if (response.headers.has("date"))
    serverClockOffset = clockOffsetFrom(response);
  const body = await readJson(response);
  if (!response.ok) {
    throw new WalletApiError(
      response.status,
      typeof body.reason_code === "string" ? body.reason_code : "unknown",
      body.retryable === true || response.status >= 500,
      guidanceOf(body),
    );
  }
  return body;
}

function operationResult(body: Record<string, unknown>): WalletOperationResult {
  return {
    operationId: String(body.operationId ?? ""),
    itemId: String(body.itemId ?? ""),
    version: String(body.version ?? ""),
    state: String(body.state ?? ""),
    auditPending: body.auditPending === true,
  };
}

/**
 * The signed-in owner's Wallet subject: the `sub` of the same access token the
 * Gateway derives the owner from. It is bound into every transport context;
 * the store refuses an envelope bound to anyone else, so a wrong value can only
 * fail closed.
 */
export async function ownerSubject(): Promise<string> {
  const token = await bearer(WALLET_API_BASE, "GET");
  const subject = subjectOfToken(token);
  if (!subject) throw new WalletApiError(401, "owner_unknown", false, null);
  return subject;
}

export function subjectOfToken(token: string): string | null {
  const payload = token.split(".")[1];
  if (!payload) return null;
  try {
    const bytes = Uint8Array.from(
      atob(payload.replace(/-/g, "+").replace(/_/g, "/")),
      (char) => char.charCodeAt(0),
    );
    const claims: unknown = JSON.parse(new TextDecoder().decode(bytes));
    const sub = (claims as { sub?: unknown } | null)?.sub;
    return typeof sub === "string" && sub.length > 0 && sub.length <= 512
      ? sub
      : null;
  } catch {
    return null;
  }
}

export async function listItems(): Promise<WalletItem[]> {
  const body = await request("items");
  if (!Array.isArray(body.items))
    throw new WalletApiError(502, "wallet_gateway_unavailable", true, null);
  return body.items.filter(
    (item): item is WalletItem =>
      Boolean(item) &&
      typeof item.itemId === "string" &&
      typeof item.itemType === "string" &&
      typeof item.displayName === "string" &&
      typeof item.scope === "string",
  );
}

/** Fetch the capture key and verify it before anything is encrypted. */
async function captureKey() {
  return verifyCaptureKey(await request("capture-key"));
}

export interface CreateItemInput {
  itemType: string;
  displayName: string;
  scope: string;
  description?: string;
  /** Required for variables and configurations, refused for other types. */
  name?: string;
  /**
   * The value to encrypt in this browser. `null` has the Wallet generate the
   * key of an SSH CA instead, so nothing is encrypted or sent.
   */
  value: string | null;
  ownerSubject: string;
  /** Sent only when it differs from the default (`read_right_holders`). */
  policy?: ItemPolicy;
  /** SSH CAs only: what the authority may sign. */
  signPolicy?: SignPolicy;
}

/**
 * Create the item (version 1): the value is encrypted in this browser and only
 * the JWE is sent, or an SSH CA is generated inside the Wallet.
 */
export async function createItem(
  input: CreateItemInput,
): Promise<WalletOperationResult> {
  let source: Record<string, unknown>;
  if (input.value === null) {
    source = {
      generate: { algorithm: "ssh-ed25519" },
      operationId: crypto.randomUUID(),
    };
  } else {
    const key = await captureKey();
    const envelope = await encryptCapture({
      captureKey: key,
      ownerSubject: input.ownerSubject,
      itemVersion: "1",
      value: input.value,
      now: serverNow(),
    });
    source = {
      capture: { jwe: envelope.jwe, expiresAt: envelope.expiresAt },
      operationId: envelope.operationId,
    };
  }
  const body = await request("items", {
    method: "POST",
    body: JSON.stringify({
      itemType: input.itemType,
      metadata: {
        displayName: input.displayName,
        scope: input.scope,
        ...(input.description ? { description: input.description } : {}),
        ...(input.name !== undefined ? { name: input.name } : {}),
      },
      ...source,
      ...(input.policy && input.policy.valueAccess !== "read_right_holders"
        ? { policy: { valueAccess: input.policy.valueAccess } }
        : {}),
      ...(input.signPolicy ? { signPolicy: input.signPolicy } : {}),
    }),
  });
  return operationResult(body);
}

export interface ConfigureItemInput {
  metadata?: {
    displayName: string;
    scope: string;
    description?: string;
    name?: string;
  };
  policy?: ItemPolicy;
  signPolicy?: SignPolicy;
}

/** Change an item's settings; only the members given are sent. */
export async function configureItem(
  itemId: string,
  changes: ConfigureItemInput,
): Promise<WalletOperationResult> {
  const sent: ConfigureItemInput = {};
  if (changes.metadata) sent.metadata = changes.metadata;
  if (changes.policy) sent.policy = { valueAccess: changes.policy.valueAccess };
  if (changes.signPolicy) sent.signPolicy = changes.signPolicy;
  const body = await request(`items/${encodeURIComponent(itemId)}/configure`, {
    method: "POST",
    body: JSON.stringify(sent),
  });
  return operationResult(body);
}

/** Encrypt a new value as the next version of an item. */
export async function addVersion(
  item: Pick<WalletItem, "itemId" | "currentVersion">,
  value: string,
  owner: string,
): Promise<WalletOperationResult> {
  const current = Number.parseInt(item.currentVersion ?? "", 10);
  if (!Number.isSafeInteger(current) || current < 1)
    throw new WalletApiError(409, "version_conflict", false, null);
  const key = await captureKey();
  const envelope = await encryptCapture({
    captureKey: key,
    ownerSubject: owner,
    itemVersion: String(current + 1),
    value,
    now: serverNow(),
  });
  const body = await request(
    `items/${encodeURIComponent(item.itemId)}/versions`,
    {
      method: "POST",
      body: JSON.stringify({
        capture: { jwe: envelope.jwe, expiresAt: envelope.expiresAt },
        operationId: envelope.operationId,
      }),
    },
  );
  return operationResult(body);
}

export interface RevealedValue {
  value: string;
  version: string;
  auditPending: boolean;
}

/**
 * Reveal the current value to a fresh ephemeral key of this browser and
 * decrypt it here. The private key never leaves the tab.
 */
export async function revealItem(
  itemId: string,
  owner: string,
): Promise<RevealedValue> {
  const recipient = await createRevealRecipient();
  const expiresAt = serverNow() + ENVELOPE_LIFETIME_MS;
  const body = await request(`items/${encodeURIComponent(itemId)}/reveal`, {
    method: "POST",
    body: JSON.stringify({ recipientKey: recipient.publicJwk, expiresAt }),
  });
  const value = await decryptReveal({
    recipient,
    response: body,
    ownerSubject: owner,
    itemId,
    expiresAt,
    now: serverNow(),
  });
  return {
    value,
    version: String(body.version ?? ""),
    auditPending: body.auditPending === true,
  };
}

export interface IssuedCertificate {
  /** The public certificate line, ready for a `-cert.pub` file. */
  certificate: string;
  serial: string;
  /** Epoch milliseconds. */
  validAfter: number;
  validBefore: number;
  principals: string[];
  auditPending: boolean;
}

/**
 * Have an SSH CA sign a public key inside its sign policy. The answer is kept
 * only if it certifies exactly the key, kind and principals that were asked
 * for.
 */
export async function signCertificate(
  itemId: string,
  signed: { request: SignRequest; subject: SubjectKey },
): Promise<IssuedCertificate> {
  const { request: asked, subject } = signed;
  const body = await request(`items/${encodeURIComponent(itemId)}/sign`, {
    method: "POST",
    body: JSON.stringify(asked),
  });
  const { certificate, serial, validAfter, validBefore, principals } = body;
  if (
    typeof certificate !== "string" ||
    typeof serial !== "string" ||
    !Number.isSafeInteger(validAfter) ||
    !Number.isSafeInteger(validBefore) ||
    !Array.isArray(principals) ||
    !principals.every((name) => typeof name === "string") ||
    body.kind !== asked.kind ||
    !certificateMatches(certificate, subject, asked.kind, asked.principals)
  )
    throw new WalletApiError(502, "sign_response_invalid", false, null);
  return {
    certificate,
    serial,
    validAfter: validAfter as number,
    validBefore: validBefore as number,
    principals: principals as string[],
    auditPending: body.auditPending === true,
  };
}

export async function revokeItem(
  itemId: string,
): Promise<WalletOperationResult> {
  return operationResult(
    await request(`items/${encodeURIComponent(itemId)}/revoke`, {
      method: "POST",
    }),
  );
}

export async function deleteItem(
  itemId: string,
): Promise<WalletOperationResult> {
  return operationResult(
    await request(`items/${encodeURIComponent(itemId)}`, { method: "DELETE" }),
  );
}

// --- product assignment (Techstack) -----------------------------------------

/** The rights the kombify products hold on one item. */
export async function listServiceAccess(
  itemId: string,
): Promise<ServiceRightsGrant[]> {
  const grants = serviceGrantsFrom(
    await request(`items/${encodeURIComponent(itemId)}/services`),
  );
  if (!grants)
    throw new WalletApiError(
      502,
      "service_access_response_invalid",
      true,
      null,
    );
  return grants;
}

/** Grant or revoke one right of one product on one item; true when held after. */
export async function setServiceAccess(
  itemId: string,
  serviceId: WalletService,
  right: WalletRight,
  granted: boolean,
): Promise<boolean> {
  const body = await request(`items/${encodeURIComponent(itemId)}/services`, {
    method: "POST",
    body: JSON.stringify({
      action: granted ? "grant" : "revoke",
      serviceId,
      right,
    }),
  });
  return body.granted === true;
}

/**
 * Make sure Techstack holds the `use` right on an item, granting it when it is
 * missing. Used where picking an item is the owner's decision to let Techstack
 * act with it (a server's access profile, a stack credential).
 */
export async function allowTechstackToUse(itemId: string): Promise<void> {
  if (techstackMayUse(await listServiceAccess(itemId))) return;
  if (!(await setServiceAccess(itemId, TECHSTACK_SERVICE, "use", true)))
    throw new WalletApiError(
      502,
      "service_access_response_invalid",
      true,
      null,
    );
}

/** The products that may add items to the signed-in person's Wallet. */
export async function listServiceCreators(): Promise<WalletService[]> {
  const services = creatorServicesFrom(await request("services/creators"));
  if (!services)
    throw new WalletApiError(
      502,
      "service_access_response_invalid",
      true,
      null,
    );
  return services;
}

/** Allow or withdraw one product's right to add items; true when allowed after. */
export async function setServiceCreator(
  serviceId: WalletService,
  granted: boolean,
): Promise<boolean> {
  const body = await request("services/creators", {
    method: "POST",
    body: JSON.stringify({
      action: granted ? "grant" : "revoke",
      serviceId,
    }),
  });
  return body.granted === true;
}
