import type { Route } from "@playwright/test";
import {
  createStoreSimulator,
  thumbprint,
  type PublicJwk,
  type StoreSimulator,
} from "../../src/lib/testing/wallet-store-simulator";

/**
 * A stand-in for the Gateway's Wallet owner API (`/v1/wallet/*`) for browser
 * specs. It checks what the real store checks that matters to the UI: the bearer
 * names the owner, a captured value must open against the transport context
 * bound to that owner, version and operation, and a reveal is sealed to the
 * ephemeral key the browser sent. Values are kept only as long as the spec runs.
 */

export const WALLET_API_ORIGIN = "https://api.kombify.io";
const REVEAL_AUDIENCE = "urn:kombify:wallet:owner-session";

export function accessTokenFor(sub: string): string {
  const part = (value: object) =>
    Buffer.from(JSON.stringify(value)).toString("base64url");
  return `${part({ alg: "none", typ: "JWT" })}.${part({
    sub,
    aud: WALLET_API_ORIGIN,
    exp: Math.floor(Date.now() / 1000) + 3600,
  })}.signature`;
}

interface StoredItem {
  itemId: string;
  itemType: string;
  displayName: string;
  scope: string;
  description?: string;
  name?: string;
  policy: { valueAccess: string; ownerProof: "none" };
  state: "active" | "revoked";
  version: number;
  value: string;
  createdAt: number;
  updatedAt: number;
}

export interface WalletRequestLog {
  method: string;
  path: string;
  /** The raw request body, kept to prove no plaintext left the browser. */
  body: string;
}

export class WalletMock {
  readonly store: StoreSimulator = createStoreSimulator({
    kid: "wallet-capture-ui",
  });
  readonly items = new Map<string, StoredItem>();
  /** itemId -> serviceId -> rights */
  readonly grants = new Map<string, Map<string, Set<string>>>();
  readonly creators = new Set<string>();
  readonly requests: WalletRequestLog[] = [];
  private counter = 0;

  constructor(readonly ownerSubject: string) {}

  private nextItemId(): string {
    this.counter += 1;
    return `itm_${"a".repeat(25)}${String.fromCharCode(96 + this.counter)}`;
  }

  /** An item that already exists in the Wallet when the page opens. */
  seed(item: {
    itemType: string;
    displayName: string;
    scope?: string;
    description?: string;
    value: string;
  }): string {
    const itemId = this.nextItemId();
    const now = Date.now();
    this.items.set(itemId, {
      itemId,
      itemType: item.itemType,
      displayName: item.displayName,
      scope: item.scope ?? "account",
      description: item.description,
      policy: { valueAccess: "read_right_holders", ownerProof: "none" },
      state: "active",
      version: 1,
      value: item.value,
      createdAt: now,
      updatedAt: now,
    });
    return itemId;
  }

  /** Let a product use an item, as the owner would have in their Wallet. */
  allow(itemId: string, serviceId: string, right: string): void {
    const rights = this.grants.get(itemId) ?? new Map<string, Set<string>>();
    rights.set(serviceId, (rights.get(serviceId) ?? new Set()).add(right));
    this.grants.set(itemId, rights);
  }

  techstackMayUse(itemId: string): boolean {
    return this.grants.get(itemId)?.get("techstack")?.has("use") ?? false;
  }

  private reply(route: Route, status: number, body: unknown) {
    return route.fulfill({
      status,
      contentType: "application/json",
      headers: {
        "access-control-allow-origin": "*",
        "access-control-allow-headers": "authorization, content-type",
        "access-control-allow-methods": "GET, POST, DELETE, OPTIONS",
        "cache-control": "no-store",
      },
      body: JSON.stringify(body),
    });
  }

  private denied(route: Route, status: number, reason: string) {
    return this.reply(route, status, {
      error_code: "WALLET_OPERATION_DENIED",
      reason_code: reason,
      retryable: false,
      user_guidance: "The Wallet refused the request.",
    });
  }

  private view(item: StoredItem) {
    return {
      itemId: item.itemId,
      itemType: item.itemType,
      displayName: item.displayName,
      scope: item.scope,
      ...(item.description ? { description: item.description } : {}),
      ...(item.name ? { name: item.name } : {}),
      policy: item.policy,
      state: item.state,
      currentVersion: String(item.version),
      createdAt: item.createdAt,
      updatedAt: item.updatedAt,
      disclosure: { sinceRotation: 0 },
    };
  }

  private ownerOf(route: Route): string | null {
    const header = route.request().headers().authorization ?? "";
    const token = header.startsWith("Bearer ") ? header.slice(7) : "";
    try {
      const claims = JSON.parse(
        Buffer.from(token.split(".")[1] ?? "", "base64url").toString("utf8"),
      ) as { sub?: string };
      return claims.sub ?? null;
    } catch {
      return null;
    }
  }

  /** Route handler for `https://api.kombify.io/v1/wallet/**`. */
  handle = async (route: Route): Promise<void> => {
    const request = route.request();
    if (request.method() === "OPTIONS") return this.reply(route, 204, {});
    const url = new URL(request.url());
    const path = url.pathname.replace(/^\/v1\/wallet\//, "");
    const method = request.method();
    const text = request.postData() ?? "";
    this.requests.push({ method, path, body: text });
    const owner = this.ownerOf(route);
    if (owner !== this.ownerSubject)
      return this.denied(route, 401, "auth_required");
    const body = text ? (JSON.parse(text) as Record<string, any>) : {};

    if (method === "GET" && path === "capture-key")
      return this.reply(route, 200, this.store.captureKey);
    if (method === "GET" && path === "items")
      return this.reply(route, 200, {
        items: [...this.items.values()].map((item) => this.view(item)),
      });
    if (method === "POST" && path === "items")
      return this.create(route, body, owner);

    if (method === "GET" && path === "services/creators")
      return this.reply(route, 200, { services: [...this.creators] });
    if (method === "POST" && path === "services/creators") {
      if (body.action === "grant") this.creators.add(body.serviceId);
      else this.creators.delete(body.serviceId);
      return this.reply(route, 200, {
        serviceId: body.serviceId,
        granted: this.creators.has(body.serviceId),
      });
    }

    const match = /^items\/(itm_[a-z2-7]{26})(?:\/(\w+))?$/.exec(path);
    const item = match ? this.items.get(match[1]) : undefined;
    if (!match || !item) return this.denied(route, 404, "item_unknown");
    const action = match[2];

    if (method === "DELETE" && !action) {
      this.items.delete(item.itemId);
      return this.reply(route, 200, this.operation(item));
    }
    if (method === "POST" && action === "revoke") {
      item.state = "revoked";
      return this.reply(route, 200, this.operation(item));
    }
    if (method === "POST" && action === "reveal")
      return this.reveal(route, body, item, owner);
    if (action === "services") {
      const rights =
        this.grants.get(item.itemId) ?? new Map<string, Set<string>>();
      this.grants.set(item.itemId, rights);
      if (method === "POST") {
        const held = rights.get(body.serviceId) ?? new Set<string>();
        if (body.action === "grant") held.add(body.right);
        else held.delete(body.right);
        rights.set(body.serviceId, held);
        return this.reply(route, 200, {
          itemId: item.itemId,
          serviceId: body.serviceId,
          right: body.right,
          granted: held.has(body.right),
        });
      }
      return this.reply(route, 200, {
        itemId: item.itemId,
        grants: [...rights.entries()].map(([serviceId, set]) => ({
          serviceId,
          rights: [...set],
        })),
      });
    }
    return this.denied(route, 404, "item_action_unsupported");
  };

  private operation(item: StoredItem) {
    return {
      operationId: crypto.randomUUID(),
      itemId: item.itemId,
      version: String(item.version),
      state: item.state,
      auditPending: false,
    };
  }

  private async create(route: Route, body: Record<string, any>, owner: string) {
    const capture = body.capture as
      { jwe: string; expiresAt: number } | undefined;
    if (!capture) return this.denied(route, 400, "capture_invalid");
    const key = this.store.captureKey as Record<string, string>;
    let value: string;
    try {
      value = this.store.openCapture(capture.jwe, {
        custodyNamespace: key.custodyNamespace,
        ownerSubject: owner,
        vaultRef: key.vaultRef,
        itemVersion: "1",
        keyGeneration: key.keyGeneration,
        operation: "capture",
        operationId: body.operationId,
        audience: key.audience,
        recipientKeyThumbprint: key.thumbprint,
        expiresAt: capture.expiresAt,
      });
    } catch {
      return this.denied(route, 400, "capture_invalid");
    }
    const itemId = this.nextItemId();
    const now = Date.now();
    const item: StoredItem = {
      itemId,
      itemType: body.itemType,
      displayName: body.metadata.displayName,
      scope: body.metadata.scope,
      description: body.metadata.description,
      name: body.metadata.name,
      policy: {
        valueAccess: body.policy?.valueAccess ?? "read_right_holders",
        ownerProof: "none",
      },
      state: "active",
      version: 1,
      value,
      createdAt: now,
      updatedAt: now,
    };
    this.items.set(itemId, item);
    return this.reply(route, 200, this.operation(item));
  }

  private async reveal(
    route: Route,
    body: Record<string, any>,
    item: StoredItem,
    owner: string,
  ) {
    const recipient = body.recipientKey as PublicJwk;
    const key = this.store.captureKey as Record<string, string>;
    const context = {
      custodyNamespace: key.custodyNamespace,
      ownerSubject: owner,
      vaultRef: key.vaultRef,
      itemVersion: String(item.version),
      keyGeneration: key.keyGeneration,
      operation: "reveal",
      operationId: crypto.randomUUID(),
      audience: REVEAL_AUDIENCE,
      recipientKeyThumbprint: thumbprint(recipient),
      expiresAt: body.expiresAt as number,
    };
    const jwe = this.store.sealReveal(item.value, recipient, context);
    return this.reply(route, 200, {
      ...this.operation(item),
      operationId: context.operationId,
      jwe,
      context,
    });
  }
}
