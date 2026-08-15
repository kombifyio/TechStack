/**
 * postMessage Bridge for Embedded Mode
 *
 * Provides bidirectional communication between the embedded app (iframe)
 * and the parent kombify Cloud Portal.
 *
 * Inbound messages (portal → app):
 *   { type: 'navigate', path: '/stacks/abc' }
 *   { type: 'refresh' }
 *   { type: 'theme', value: 'dark' | 'light' }
 *     with optional systemCardShape: 'square' | 'app'
 *   { type: 'identity', value: { name, characterId, animationStyle } }
 *   { type: 'auth-token', token: '<sso-jwt>' }
 *   { type: 'auth-error', error: '...' }
 *   { type: 'gateway-token', token: '<auth0-api-access-token>', audience: 'https://api.kombify.io', expiresAt?: number }
 *   { type: 'gateway-token-error', error: '...' }
 *   { type: 'ai-handover-ack', correlation_id: '...', status: 'accepted' | 'session-ready' | 'failed', ... }
 *
 * Outbound messages (app → portal):
 *   { type: 'ready', tool: 'kombifystack' }
 *   { type: 'navigate', path: '/some/portal/path' }
 *   { type: 'resize', height: number }
 *   { type: 'auth-request', tool: 'kombifystack' }
 *   { type: 'gateway-token-request', tool: 'kombifystack', audience: 'https://api.kombify.io' }
 *   { type: 'ai-handover', tool: 'kombifystack', ... }
 */

import { browser } from "$app/environment";
import { goto } from "$app/navigation";
import { theme } from "./theme";
import { setStackIdentity, type StackIdentity } from "./stackIdentity";

const TOOL_ID = "kombifystack";
const GATEWAY_AUDIENCE = "https://api.kombify.io";
const AUTH_REQUEST_TIMEOUT_MS = 10_000;
const GATEWAY_TOKEN_REQUEST_TIMEOUT_MS = 10_000;
const AUTH_TOKEN_CACHE_MS = 4 * 60_000;
const GATEWAY_TOKEN_CACHE_SKEW_MS = 30_000;
const AI_HANDOVER_REQUEST_TIMEOUT_MS = 70_000;
const AI_HANDOVER_ACK_SCHEMA = "kombify.ai-handover-ack.v1";
// Floor between parent SSO refreshes. The parent caps /api/tools/sso-refresh
// at 10/user/60s; staying above 6s/req keeps a persistent auth failure from
// bursting past that cap into a 429 loop.
const MIN_AUTH_REQUEST_INTERVAL_MS = 6_500;
// The first request can be lost while the parent shell is mounting. Re-send
// only once after the portal's documented rate-limit floor, while retaining a
// finite overall wait so an unavailable parent cannot create a retry loop.
const AUTH_REQUEST_RETRY_DELAY_MS = MIN_AUTH_REQUEST_INTERVAL_MS;
const DEFAULT_PORTAL_ORIGINS = ["https://kombify.io", "https://kombify.dev"];

interface PortalMessage {
  type: string;
  [key: string]: unknown;
}

export interface AiErrorHandoverRequest {
  schema: "kombify.ai-handover.v1";
  targetPanelMode: "support";
  agentId: "kombify-support";
  prompt: string;
  errorContext: Record<string, unknown>;
}

export interface AiErrorHandoverFailure {
  code: string;
  message: string;
  retryable: boolean;
}

export type AiErrorHandoverUpdate =
  | { status: "accepted"; duplicate?: true }
  | { status: "session-ready"; sessionId: string; duplicate?: true }
  | {
      status: "failed";
      error: AiErrorHandoverFailure;
      duplicate?: true;
    };

let initialized = false;
let allowedOrigins = new Set<string>();
let targetOrigin: string | null = null;
let resizeObserver: ResizeObserver | null = null;
const pendingAuthWaiters: Array<{
  resolve: (token: string) => void;
  reject: (error: Error) => void;
}> = [];
const pendingGatewayTokenWaiters: Array<{
  resolve: (token: string) => void;
  reject: (error: Error) => void;
}> = [];
let authRequestTimeout: ReturnType<typeof setTimeout> | null = null;
let authRequestRetryTimeout: ReturnType<typeof setTimeout> | null = null;
let gatewayTokenRequestTimeout: ReturnType<typeof setTimeout> | null = null;
let lastAuthRequestSentAt = 0;
let cachedAuthToken: { token: string; receivedAt: number } | null = null;
let cachedGatewayToken: {
  token: string;
  audience: string;
  expiresAt?: number;
  receivedAt: number;
} | null = null;
const pendingAiHandovers = new Map<
  string,
  {
    listeners: Set<(update: AiErrorHandoverUpdate) => void>;
    timeout: ReturnType<typeof setTimeout>;
  }
>();

function normalizeOrigin(raw: string | null | undefined): string | null {
  if (!raw) return null;

  try {
    const url = new URL(raw);
    if (url.protocol === "http:" || url.protocol === "https:") {
      return url.origin;
    }
  } catch {
    return null;
  }

  return null;
}

/**
 * Initialize the postMessage bridge. Only activates when running in an iframe.
 * @param portalOrigin — The exact expected origin of the parent portal.
 *                        If not provided, only known default origins or local dev origins are inferred.
 */
export function initBridge(portalOrigin?: string): void {
  if (!browser || initialized) return;
  if (window.parent === window) return; // Not in iframe

  initialized = true;
  const origins = resolvePortalOrigins(portalOrigin);
  allowedOrigins = new Set(origins);
  targetOrigin = origins[0] ?? null;

  window.addEventListener("message", handleMessage);

  // Notify parent that the app is ready
  sendToPortal({ type: "ready", tool: TOOL_ID });

  // Auto-resize: observe document height changes and notify parent
  startResizeObserver();
}

/** Tear down the bridge (for cleanup). */
export function destroyBridge(): void {
  if (!browser || !initialized) return;
  initialized = false;
  allowedOrigins = new Set();
  targetOrigin = null;
  window.removeEventListener("message", handleMessage);
  resizeObserver?.disconnect();
  resizeObserver = null;
  rejectAuthWaiters(new Error("Bridge destroyed"));
  rejectGatewayTokenWaiters(new Error("Bridge destroyed"));
  clearPendingAiHandovers();
}

/** Send a message to the parent portal. */
export function sendToPortal(msg: PortalMessage): void {
  if (!browser || window.parent === window) return;
  if (!targetOrigin) {
    const origins = resolvePortalOrigins();
    allowedOrigins = new Set(origins);
    targetOrigin = origins[0] ?? null;
  }
  if (!targetOrigin) return;
  window.parent.postMessage(msg, targetOrigin);
}

/** Request the portal to navigate to a path. */
export function requestPortalNavigation(path: string): void {
  sendToPortal({ type: "navigate", path });
}

/**
 * Resolve the parent portal origin so the embedded app can open a top-level
 * re-authentication window when an in-iframe SSO refresh fails.
 *
 * The tool iframe is sandboxed with `allow-popups-to-escape-sandbox` but
 * WITHOUT top-navigation, so a popup to this origin is the only way for the
 * user to revive a dead portal session from inside the frame. Returns the
 * pinned target when the bridge is initialised, otherwise falls back to the
 * inferred origin (ancestorOrigins -> referrer -> known defaults).
 */
export function getPortalOrigin(): string | null {
  if (!browser) return null;
  if (targetOrigin) return targetOrigin;
  const [origin] = resolvePortalOrigins();
  return origin ?? null;
}

/**
 * True when an AI handover can actually reach kombify Cloud. Standalone
 * sessions (techstack.kombify.io opened directly) have no parent shell and
 * nothing listens for the fallback event, so callers must not offer the
 * action there.
 */
export function aiErrorHandoverAvailable(): boolean {
  return browser && window.parent !== window;
}

/** Ask the parent kombify Cloud shell to open its AI panel with error context. */
export function requestAiErrorHandover(
  request: AiErrorHandoverRequest,
  onUpdate: (update: AiErrorHandoverUpdate) => void = () => undefined,
): boolean {
  if (!browser) return false;

  const message = buildAiErrorHandoverMessage(request);

  if (window.parent !== window) {
    const correlationId = createAiHandoverCorrelation(message);
    trackAiHandover(correlationId, onUpdate);
    sendToPortal(message);
    return true;
  }

  window.dispatchEvent(
    new CustomEvent("kombify:ai-handover", { detail: message }),
  );
  return false;
}

export function createAiErrorHandoverCorrelation(
  request: AiErrorHandoverRequest,
): string {
  return createAiHandoverCorrelation(buildAiErrorHandoverMessage(request));
}

export function cancelAiErrorHandoverUpdates(
  request: AiErrorHandoverRequest,
  listener: (update: AiErrorHandoverUpdate) => void,
): void {
  const correlationId = createAiErrorHandoverCorrelation(request);
  const pending = pendingAiHandovers.get(correlationId);
  if (!pending) return;
  pending.listeners.delete(listener);
  if (pending.listeners.size > 0) return;
  clearTimeout(pending.timeout);
  pendingAiHandovers.delete(correlationId);
}

function buildAiErrorHandoverMessage(
  request: AiErrorHandoverRequest,
): PortalMessage {
  return {
    type: "ai-handover",
    tool: TOOL_ID,
    schema: request.schema,
    target_panel: "floating",
    target_panel_mode: request.targetPanelMode,
    agent_id: request.agentId,
    prompt: request.prompt,
    error_context: stripUndefined(request.errorContext),
  };
}

export function requestAuthToken(): Promise<string> {
  if (!browser || window.parent === window) {
    return Promise.reject(new Error("Embedded auth bridge unavailable"));
  }

  if (
    cachedAuthToken &&
    Date.now() - cachedAuthToken.receivedAt < AUTH_TOKEN_CACHE_MS
  ) {
    return Promise.resolve(cachedAuthToken.token);
  }

  // Throttle sequential refreshes (concurrent callers are coalesced below)
  // so a stuck 401 cannot spam the parent into a 429 rate-limit loop.
  if (
    pendingAuthWaiters.length === 0 &&
    Date.now() - lastAuthRequestSentAt < MIN_AUTH_REQUEST_INTERVAL_MS
  ) {
    return Promise.reject(new Error("Auth token request throttled"));
  }

  return new Promise((resolve, reject) => {
    const isFirstWaiter = pendingAuthWaiters.length === 0;
    pendingAuthWaiters.push({ resolve, reject });

    if (!isFirstWaiter) return;

    sendAuthRequestToPortal();
    authRequestRetryTimeout = setTimeout(() => {
      if (pendingAuthWaiters.length === 0) return;
      sendAuthRequestToPortal();
    }, AUTH_REQUEST_RETRY_DELAY_MS);
    authRequestTimeout = setTimeout(() => {
      rejectAuthWaiters(new Error("Auth token request timed out"));
    }, AUTH_REQUEST_TIMEOUT_MS);
  });
}

export function requestGatewayToken(
  audience: string = GATEWAY_AUDIENCE,
): Promise<string> {
  if (!browser || window.parent === window) {
    return Promise.reject(new Error("Embedded gateway bridge unavailable"));
  }

  const now = Date.now();
  if (
    cachedGatewayToken &&
    cachedGatewayToken.audience === audience &&
    (!cachedGatewayToken.expiresAt ||
      now < cachedGatewayToken.expiresAt - GATEWAY_TOKEN_CACHE_SKEW_MS)
  ) {
    return Promise.resolve(cachedGatewayToken.token);
  }

  return new Promise((resolve, reject) => {
    const isFirstWaiter = pendingGatewayTokenWaiters.length === 0;
    pendingGatewayTokenWaiters.push({ resolve, reject });

    if (!isFirstWaiter) return;

    sendToPortal({ type: "gateway-token-request", tool: TOOL_ID, audience });
    gatewayTokenRequestTimeout = setTimeout(() => {
      rejectGatewayTokenWaiters(new Error("Gateway token request timed out"));
    }, GATEWAY_TOKEN_REQUEST_TIMEOUT_MS);
  });
}

// ── Internal ────────────────────────────────────────────────

function handleMessage(event: MessageEvent): void {
  if (event.source !== window.parent) return;

  // Validate origin against the inferred/configured parent portal allow-list.
  if (allowedOrigins.size > 0 && !allowedOrigins.has(event.origin)) return;

  const data = event.data as PortalMessage;
  if (!data || typeof data.type !== "string") return;

  switch (data.type) {
    case "navigate":
      if (
        typeof data.path === "string" &&
        data.path.startsWith("/") &&
        !data.path.includes("://")
      ) {
        goto(data.path);
      }
      break;

    case "refresh":
      window.location.reload();
      break;

    case "theme":
      if (data.value === "dark" || data.value === "light") {
        theme.set(data.value);
      }
      if (data.systemCardShape === "square" || data.systemCardShape === "app") {
        theme.setSystemCardShape(data.systemCardShape);
      }
      break;

    case "identity":
      setStackIdentity(isStackIdentity(data.value) ? data.value : null);
      break;

    case "auth-token":
      if (typeof data.token === "string") {
        cachedAuthToken = { token: data.token, receivedAt: Date.now() };
        resolveAuthWaiters(data.token);
      }
      break;

    case "gateway-token":
      if (
        typeof data.token === "string" &&
        (typeof data.audience !== "string" ||
          data.audience === GATEWAY_AUDIENCE)
      ) {
        cachedGatewayToken = {
          token: data.token,
          audience:
            typeof data.audience === "string"
              ? data.audience
              : GATEWAY_AUDIENCE,
          expiresAt:
            typeof data.expiresAt === "number" ? data.expiresAt : undefined,
          receivedAt: Date.now(),
        };
        resolveGatewayTokenWaiters(data.token);
      }
      break;

    case "auth-error":
      cachedAuthToken = null;
      rejectAuthWaiters(
        new Error(
          typeof data.error === "string" ? data.error : "Authentication failed",
        ),
      );
      break;

    case "gateway-token-error":
      cachedGatewayToken = null;
      rejectGatewayTokenWaiters(
        new Error(
          typeof data.error === "string"
            ? data.error
            : "Gateway authentication failed",
        ),
      );
      break;

    case "ai-handover-ack":
      handleAiHandoverAcknowledgement(data);
      break;
  }
}

function handleAiHandoverAcknowledgement(data: PortalMessage): void {
  if (
    data.tool !== TOOL_ID ||
    data.schema !== AI_HANDOVER_ACK_SCHEMA ||
    data.handover_schema !== "kombify.ai-handover.v1" ||
    typeof data.correlation_id !== "string"
  ) {
    return;
  }

  const pending = pendingAiHandovers.get(data.correlation_id);
  if (!pending) return;

  const duplicate =
    data.duplicate === true ? ({ duplicate: true } as const) : {};
  if (data.status === "accepted") {
    notifyAiHandoverListeners(pending, { status: "accepted", ...duplicate });
    return;
  }

  if (
    data.status === "session-ready" &&
    typeof data.session_id === "string" &&
    data.session_id.trim().length > 0
  ) {
    completeAiHandover(data.correlation_id, {
      status: "session-ready",
      sessionId: data.session_id.trim(),
      ...duplicate,
    });
    return;
  }

  const failure = parseAiHandoverFailure(data.error);
  if (data.status === "failed" && failure) {
    completeAiHandover(data.correlation_id, {
      status: "failed",
      error: failure,
      ...duplicate,
    });
  }
}

function parseAiHandoverFailure(value: unknown): AiErrorHandoverFailure | null {
  if (!value || typeof value !== "object") return null;
  const candidate = value as Record<string, unknown>;
  if (
    typeof candidate.code !== "string" ||
    typeof candidate.message !== "string" ||
    typeof candidate.retryable !== "boolean"
  ) {
    return null;
  }

  return {
    code: candidate.code.slice(0, 160),
    message: candidate.message.slice(0, 400),
    retryable: candidate.retryable,
  };
}

function trackAiHandover(
  correlationId: string,
  listener: (update: AiErrorHandoverUpdate) => void,
): void {
  const existing = pendingAiHandovers.get(correlationId);
  if (existing) {
    existing.listeners.add(listener);
    return;
  }

  const timeout = setTimeout(() => {
    completeAiHandover(correlationId, {
      status: "failed",
      error: {
        code: "handover_timeout",
        message: "Kombify AI did not confirm the support session in time.",
        retryable: true,
      },
    });
  }, AI_HANDOVER_REQUEST_TIMEOUT_MS);
  pendingAiHandovers.set(correlationId, {
    listeners: new Set([listener]),
    timeout,
  });
}

function completeAiHandover(
  correlationId: string,
  update: AiErrorHandoverUpdate,
): void {
  const pending = pendingAiHandovers.get(correlationId);
  if (!pending) return;
  clearTimeout(pending.timeout);
  pendingAiHandovers.delete(correlationId);
  notifyAiHandoverListeners(pending, update);
}

function notifyAiHandoverListeners(
  pending: { listeners: Set<(update: AiErrorHandoverUpdate) => void> },
  update: AiErrorHandoverUpdate,
): void {
  for (const listener of pending.listeners) {
    try {
      listener(update);
    } catch {
      // A rendering callback must not break the authenticated bridge.
    }
  }
}

function clearPendingAiHandovers(): void {
  for (const pending of pendingAiHandovers.values()) {
    clearTimeout(pending.timeout);
  }
  pendingAiHandovers.clear();
}

function stripUndefined(
  value: Record<string, unknown>,
): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(value).filter(([, entry]) => entry !== undefined),
  );
}

function createAiHandoverCorrelation(message: PortalMessage): string {
  const canonical = stableStringify(message);
  let first = 0x811c9dc5;
  let second = 0x9e3779b9;
  for (let index = 0; index < canonical.length; index += 1) {
    const char = canonical.charCodeAt(index);
    first = Math.imul(first ^ char, 0x01000193);
    second = Math.imul(second ^ char, 0x85ebca6b);
  }
  return `tsh_${(first >>> 0).toString(16).padStart(8, "0")}${(second >>> 0)
    .toString(16)
    .padStart(8, "0")}`;
}

function stableStringify(value: unknown): string {
  if (value === null || typeof value !== "object") {
    return JSON.stringify(value) ?? "undefined";
  }
  if (Array.isArray(value)) {
    return `[${value.map(stableStringify).join(",")}]`;
  }

  const object = value as Record<string, unknown>;
  return `{${Object.keys(object)
    .sort()
    .map((key) => `${JSON.stringify(key)}:${stableStringify(object[key])}`)
    .join(",")}}`;
}

function isStackIdentity(value: unknown): value is StackIdentity {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Record<string, unknown>;
  return (
    typeof candidate.name === "string" &&
    typeof candidate.characterId === "string" &&
    typeof candidate.animationStyle === "string"
  );
}

function resolvePortalOrigins(portalOrigin?: string): string[] {
  const origins: string[] = [];
  const addConfigured = (origin: string | null) => {
    if (origin && !origins.includes(origin)) origins.push(origin);
  };

  const explicitOrigin = normalizeOrigin(portalOrigin);
  if (explicitOrigin) return [explicitOrigin];

  const ancestorOrigins = window.location.ancestorOrigins;
  if (ancestorOrigins && ancestorOrigins.length > 0) {
    const ancestorOrigin = normalizeOrigin(ancestorOrigins[0]);
    if (ancestorOrigin) return [ancestorOrigin];
  }

  const referrerOrigin = normalizeOrigin(document.referrer);
  if (referrerOrigin) return [referrerOrigin];

  for (const origin of DEFAULT_PORTAL_ORIGINS) {
    addConfigured(normalizeOrigin(origin));
  }

  return origins;
}

function startResizeObserver(): void {
  resizeObserver = new ResizeObserver(() => {
    const height = document.documentElement.scrollHeight;
    sendToPortal({ type: "resize", height });
  });
  resizeObserver.observe(document.documentElement);
}

function resolveAuthWaiters(token: string): void {
  if (authRequestTimeout) {
    clearTimeout(authRequestTimeout);
    authRequestTimeout = null;
  }
  if (authRequestRetryTimeout) {
    clearTimeout(authRequestRetryTimeout);
    authRequestRetryTimeout = null;
  }

  const waiters = pendingAuthWaiters.splice(0);
  for (const waiter of waiters) {
    waiter.resolve(token);
  }
}

function rejectAuthWaiters(error: Error): void {
  if (authRequestTimeout) {
    clearTimeout(authRequestTimeout);
    authRequestTimeout = null;
  }
  if (authRequestRetryTimeout) {
    clearTimeout(authRequestRetryTimeout);
    authRequestRetryTimeout = null;
  }

  const waiters = pendingAuthWaiters.splice(0);
  for (const waiter of waiters) {
    waiter.reject(error);
  }
}

function sendAuthRequestToPortal(): void {
  lastAuthRequestSentAt = Date.now();
  sendToPortal({ type: "auth-request", tool: TOOL_ID });
}

function resolveGatewayTokenWaiters(token: string): void {
  if (gatewayTokenRequestTimeout) {
    clearTimeout(gatewayTokenRequestTimeout);
    gatewayTokenRequestTimeout = null;
  }

  const waiters = pendingGatewayTokenWaiters.splice(0);
  for (const waiter of waiters) {
    waiter.resolve(token);
  }
}

function rejectGatewayTokenWaiters(error: Error): void {
  if (gatewayTokenRequestTimeout) {
    clearTimeout(gatewayTokenRequestTimeout);
    gatewayTokenRequestTimeout = null;
  }

  const waiters = pendingGatewayTokenWaiters.splice(0);
  for (const waiter of waiters) {
    waiter.reject(error);
  }
}
