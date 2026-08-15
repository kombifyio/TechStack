/**
 * kombify-TechStack API Client - Core Utilities
 *
 * Low-level fetch wrapper and helper functions for API communication.
 * Handles communication with the Go backend.
 */

import { get as readStore } from "svelte/store";
import { getClientBootstrap } from "$lib/client/bootstrap";
import { getPocketBaseCompatStoredAuthToken } from "$lib/auth/pocketbase-compat";
import { deploymentMode } from "$lib/stores/deploymentMode";
import { getGatewayToken } from "$lib/auth/gateway-auth";
import { noteGatewayTokenSuccess } from "$lib/auth/session-recovery";
import {
  isSessionReprojectionError,
  recoverFromSessionReprojection,
} from "$lib/api/session-reprojection";

// IMPORTANT:
// - In the browser we prefer relative URLs so the deployment can decide routing
//   (reverse proxy, same-origin, etc.) without hardcoding any host/port.
// - For SSR, provide an absolute base URL via env (e.g. VITE_API_URL at build
//   time or TECHSTACK_API_URL at runtime).
const ENV_API_BASE =
  import.meta.env.VITE_API_URL ||
  (typeof process !== "undefined" && process.env
    ? process.env.TECHSTACK_API_URL || ""
    : "");

function isIpHost(hostname: string): boolean {
  // IPv4
  if (/^\d{1,3}(?:\.\d{1,3}){3}$/.test(hostname)) return true;
  // IPv6 (very loose check, but good enough for routing decisions)
  if (hostname.includes(":")) return true;
  return false;
}

function stripTrailingSlash(url: string): string {
  return url ? url.replace(/\/+$/, "") : "";
}

/**
 * API base URL resolution.
 *
 * We keep same-origin by default (API_BASE="") to support reverse proxies.
 * For local dev where the UI runs on a different port than the backend,
 * provide `VITE_API_URL` (recommended). If missing, we fall back to
 * heuristics (localhost:5261 by default).
 */
export const API_BASE = (() => {
  if (ENV_API_BASE) {
    // In the browser, ignore internal Docker hostnames like `http://techstack:5260`
    // because the browser can't resolve them. Still allow explicit same-origin,
    // localhost, IPs, or real domains.
    if (typeof window !== "undefined") {
      const envOrigin = safeOrigin(ENV_API_BASE);
      if (envOrigin) {
        try {
          const envUrl = new URL(envOrigin);
          const hostname = envUrl.hostname;
          const isSameOrigin = envOrigin === window.location.origin;
          const isLocalhost = isLocalhostOrigin(envOrigin);
          const isRealDomain = hostname.includes(".");
          const isIp = isIpHost(hostname);

          if (isSameOrigin || isLocalhost || isRealDomain || isIp) {
            return stripTrailingSlash(ENV_API_BASE);
          }
          // fall through to normal browser heuristics
        } catch {
          // fall through
        }
      } else {
        // Non-URL value; keep previous behavior.
        return stripTrailingSlash(ENV_API_BASE);
      }
    } else {
      return stripTrailingSlash(ENV_API_BASE);
    }
  }

  // Browser:
  // - In production / embedded mode, prefer same-origin so reverse proxies and
  //   cookie policies work naturally.
  // - In Vite dev, the UI runs on a different port (e.g. 5173/5271) and needs
  //   an explicit backend origin.
  if (typeof window !== "undefined") {
    const uiOrigin = window.location.origin;
    const uiPort = (() => {
      try {
        return new URL(uiOrigin).port;
      } catch {
        return "";
      }
    })();

    const isViteDevPort = uiPort === "5173" || uiPort === "5271";

    // In Vite dev, prefer a stable default (localhost:5261) unless explicitly
    // configured via env. Do NOT default to the PocketBase origin (often :5260),
    // because core API routes live on the Go backend.
    if (isViteDevPort) {
      try {
        const u = new URL(uiOrigin);
        u.port = "5261";
        return u.origin;
      } catch {
        // fall through
      }
    }

    return ""; // same-origin
  }

  // SSR: try environment first; otherwise fall back to a sensible default.
  const env = typeof process !== "undefined" && process.env ? process.env : {};
  const runtimeBase = env.TECHSTACK_API_URL || "";
  if (runtimeBase) return stripTrailingSlash(runtimeBase);

  const hostname = typeof env.HOSTNAME === "string" ? env.HOSTNAME : "";
  const looksLikeDocker = hostname.length >= 8;
  return looksLikeDocker ? "http://techstack:5261" : "http://localhost:5261";
})();

// Cloudflare gateway base for the embedded data plane, e.g.
// https://api.kombify.io/v1/techstack. Set at build time (VITE_GATEWAY_API_BASE).
const GATEWAY_API_BASE =
  (import.meta.env.VITE_GATEWAY_API_BASE as string | undefined)?.replace(
    /\/+$/,
    "",
  ) || "";
// Runtime edition (formerly PUBLIC_KOMBIFY_EDITION via $env/dynamic/public).
// Read per call from the client bootstrap snapshot so the static bundle stays
// deployment-agnostic; before the bootstrap fetch resolves this is "" and
// gateway routing falls back to the deployment-mode store, matching the
// pre-existing async-config behavior.
function runtimeKombifyEdition(): string {
  return getClientBootstrap().kombifyEdition;
}

/**
 * In SaaS data-plane mode, route authenticated /api/v1/* calls through the
 * Cloudflare gateway (api.kombify.io/v1/techstack/*) so the edge validates the
 * user's Auth0 token and signs the entitlement-flag envelope. The gateway strips
 * /v1/techstack back to /api/v1 at the origin, so /api/v1/x maps to
 * `${gatewayBase}/x`.
 *
 * Pure for testability. Returns null when the call must stay same-origin:
 * a candidate preview, self-hosted data plane, no gateway base configured, an
 * absolute URL, or a bootstrap/auth endpoint that must establish the browser
 * session locally.
 */
export function resolveGatewayUrl(
  endpoint: string,
  opts: {
    mode: string;
    gatewayBase: string;
    dataPlane?: string;
    kombifyEdition?: string;
    method?: string;
  },
): string | null {
  if (!opts.gatewayBase) return null;
  const edition = opts.kombifyEdition?.trim().toLowerCase();
  // Render PR previews must exercise the UI and API from the same candidate
  // revision. Routing them through the production gateway would validate the
  // preview UI against whichever TechStack revision happens to be live.
  if (edition === "preview") return null;
  const shouldUseGateway =
    opts.dataPlane === "gateway" ||
    opts.mode === "saas-embedded" ||
    edition === "cloud" ||
    edition === "saas-standalone";
  if (!shouldUseGateway) return null;
  if (endpoint.startsWith("http://") || endpoint.startsWith("https://"))
    return null;
  if (endpoint !== "/api/v1" && !endpoint.startsWith("/api/v1/")) return null;
  if (mustStaySameOrigin(endpoint, opts.method)) return null;
  return `${opts.gatewayBase}${endpoint.slice("/api/v1".length)}`;
}

function gatewayUrlFor(endpoint: string, method?: string): string | null {
  if (typeof window === "undefined") return null;
  const config = readStore(deploymentMode);
  return resolveGatewayUrl(endpoint, {
    mode: config.mode,
    dataPlane: config.dataPlane,
    gatewayBase: GATEWAY_API_BASE,
    kombifyEdition: runtimeKombifyEdition(),
    method,
  });
}

async function getParentBridgeGatewayToken(): Promise<string> {
  if (typeof window === "undefined" || window.parent === window) {
    throw new Error("embedded_parent_bridge_unavailable");
  }

  const { initBridge, requestGatewayToken } =
    await import("$lib/stores/postMessageBridge");
  initBridge();
  const token = await requestGatewayToken();
  if (!token) {
    throw new Error("embedded_parent_bridge_token_empty");
  }
  return token;
}

function mustStaySameOrigin(endpoint: string, _method?: string): boolean {
  const path = endpoint.split(/[?#]/, 1)[0] || endpoint;
  return (
    path === "/api/v1/csrf" ||
    path === "/api/v1/info" ||
    path === "/api/v1/health" ||
    path === "/api/v1/instance" ||
    path === "/api/v1/auth" ||
    path.startsWith("/api/v1/auth/")
  );
}

export function safeOrigin(raw: string): string {
  try {
    return new URL(raw).origin;
  } catch {
    return "";
  }
}

export function isLocalhostOrigin(origin: string): boolean {
  try {
    const u = new URL(origin);
    return (
      u.hostname === "localhost" ||
      u.hostname === "127.0.0.1" ||
      u.hostname === "::1"
    );
  } catch {
    return false;
  }
}

// Best-effort URL selection for commands that must run on a different machine.
// Priority: current browser origin (if not localhost) -> configured API base
// (if not localhost) -> browser origin.
export function getBestPublicServerUrl(): string {
  const browserOrigin =
    typeof window !== "undefined" ? window.location.origin : "";
  const apiOrigin = safeOrigin(API_BASE);

  const candidates = [browserOrigin, apiOrigin, browserOrigin]
    .map((s) => s.replace(/\/$/, ""))
    .filter(Boolean);

  const preferred = candidates.find((c) => !isLocalhostOrigin(c));
  return (preferred || candidates[0] || "").replace(/\/$/, "");
}

export interface ApiResponse<T> {
  data: T;
  meta?: {
    total?: number;
    page?: number;
    per_page?: number;
  };
}

export interface ApiError {
  error: {
    code: string;
    message: string;
    details?: unknown;
  };
}

export class ApiRequestError extends Error {
  status?: number;
  code?: string;
  url?: string;
  method?: string;
  contentType?: string;
  requestId?: string;
  responseBodySnippet?: string;
  details?: unknown;
  retryable?: boolean;

  constructor(message: string, init?: Partial<ApiRequestError>) {
    super(message);
    this.name = "ApiRequestError";
    Object.assign(this, init);
  }
}

const CSRF_HEADER_NAME = "X-CSRF-Token";
let csrfToken: string | null = null;
let csrfTokenInFlight: Promise<string> | null = null;

function normalizeMethod(method: string | undefined): string {
  return (method || "GET").toUpperCase();
}

function needsCSRF(method: string): boolean {
  return !["GET", "HEAD", "OPTIONS", "TRACE"].includes(method);
}

function csrfEndpointFor(url: string): string {
  if (url.startsWith("http://") || url.startsWith("https://")) {
    return new URL("/api/v1/csrf", url).toString();
  }
  return "/api/v1/csrf";
}

async function getCSRFToken(url: string): Promise<string> {
  if (csrfToken) return csrfToken;
  if (csrfTokenInFlight) return csrfTokenInFlight;

  csrfTokenInFlight = fetch(csrfEndpointFor(url), {
    method: "GET",
    credentials: "include",
  })
    .then(async (response) => {
      if (!response.ok) {
        throw await parseApiErrorResponse(response, {
          action: "CSRF token request failed",
          method: "GET",
        });
      }
      const token = response.headers.get(CSRF_HEADER_NAME);
      if (token) return token;
      const payload = (await response.json()) as { token?: string };
      if (!payload.token) throw new Error("CSRF token response missing token");
      return payload.token;
    })
    .then((token) => {
      csrfToken = token;
      return token;
    })
    .finally(() => {
      csrfTokenInFlight = null;
    });

  return csrfTokenInFlight;
}

function extractRequestId(headers: Headers): string {
  return (
    headers.get("x-request-id") ||
    headers.get("x-correlation-id") ||
    headers.get("x-trace-id") ||
    ""
  );
}

function looksLikeHtml(body: string): boolean {
  const t = body.trimStart().toLowerCase();
  return t.startsWith("<!doctype") || t.startsWith("<html");
}

function safeTruncate(text: string, max: number): string {
  if (!text) return "";
  return text.length > max ? `${text.slice(0, max)}\n…(truncated)` : text;
}

export async function parseApiErrorResponse(
  response: Response,
  context?: { action?: string; method?: string },
): Promise<ApiRequestError> {
  const contentType = response.headers.get("content-type") || "";
  const requestId = extractRequestId(response.headers);

  let bodyText = "";
  try {
    bodyText = await response.text();
  } catch {
    // ignore
  }

  const basePrefix = context?.action ? `${context.action}: ` : "";
  let message = `${basePrefix}${response.status} ${response.statusText}`.trim();
  let details: unknown = undefined;
  let code: string | undefined;

  // Prefer structured API errors when available.
  const maybeJson = bodyText.trimStart();
  const mayBeJson =
    contentType.includes("application/json") ||
    maybeJson.startsWith("{") ||
    maybeJson.startsWith("[");

  if (mayBeJson) {
    try {
      const data = JSON.parse(bodyText);
      const apiErr = data as Partial<ApiError> & {
        message?: string;
        error?: any;
      };
      code = typeof apiErr?.error?.code === "string" ? apiErr.error.code : code;
      message =
        apiErr?.error?.message ||
        (typeof apiErr?.message === "string" ? apiErr.message : "") ||
        message;
      details = apiErr?.error?.details ?? apiErr?.error ?? data;
    } catch {
      // fall back to HTML/plaintext detection
    }
  }

  if (looksLikeHtml(bodyText) || contentType.includes("text/html")) {
    // This usually means a reverse proxy, auth redirect, or misrouted request.
    message =
      `${basePrefix}Server returned HTML instead of JSON (${response.status}). ` +
      `Is the backend reachable and are you authenticated?`;
    if ([502, 503, 504].includes(response.status)) {
      code = "upstream_unavailable";
    }
  } else if (!mayBeJson && bodyText.trim()) {
    // Plain-text error; surface a short snippet.
    message = `${basePrefix}${safeTruncate(bodyText.trim(), 180)}`;
  }

  return new ApiRequestError(message, {
    status: response.status,
    code,
    url: response.url,
    method: context?.method,
    contentType,
    requestId,
    responseBodySnippet: safeTruncate(bodyText, 2000),
    details,
    retryable:
      response.status === 429 || [502, 503, 504].includes(response.status),
  });
}

export interface ApiRequestOptions extends RequestInit {
  timeoutMs?: number;
  /**
   * Internal: set on the single automatic replay after an embedded
   * session-reprojection recovery so the interceptor never loops.
   */
  sessionReprojectionReplay?: boolean;
}

export async function fetchApi<T>(
  endpoint: string,
  options: ApiRequestOptions = {},
): Promise<ApiResponse<T>> {
  const {
    timeoutMs = 10_000,
    sessionReprojectionReplay = false,
    ...fetchOptions
  } = options;

  const sameOriginUrl =
    endpoint.startsWith("http://") || endpoint.startsWith("https://")
      ? endpoint
      : API_BASE
        ? `${API_BASE}${endpoint}`
        : endpoint;
  const method = normalizeMethod(fetchOptions.method);

  // SaaS data-plane calls must go through the Cloudflare gateway with a real
  // Auth0 API token so the edge signs identity and entitlement flags.
  const gatewayUrl = gatewayUrlFor(endpoint, method);
  let gatewayToken = "";
  if (gatewayUrl) {
    try {
      gatewayToken = await getGatewayToken();
      noteGatewayTokenSuccess();
    } catch (err) {
      try {
        gatewayToken = await getParentBridgeGatewayToken();
      } catch (bridgeErr) {
        throw new ApiRequestError(
          "Gateway authentication unavailable. Sign in again to verify your managed runtime entitlements.",
          {
            status: 401,
            code: "gateway_auth_unavailable",
            url: gatewayUrl,
            method,
            details: {
              auth0:
                err instanceof Error ? err.message : "gateway token failed",
              parentBridge:
                bridgeErr instanceof Error
                  ? bridgeErr.message
                  : "parent gateway token failed",
            },
          },
        );
      }
    }
  }

  const url = gatewayUrl ? gatewayUrl : sameOriginUrl;
  const authHeader: Record<string, string> = {};
  if (gatewayUrl) {
    authHeader.Authorization = `Bearer ${gatewayToken}`;
  } else if (typeof window !== "undefined") {
    // Legacy compatibility token while same-origin auth migrates to Go sessions.
    const token = getPocketBaseCompatStoredAuthToken();
    if (token) authHeader.Authorization = `Bearer ${token}`;
  }

  const headers = new Headers(fetchOptions.headers);
  headers.set(
    "Content-Type",
    headers.get("Content-Type") || "application/json",
  );
  for (const [key, value] of Object.entries(authHeader)) {
    if (!headers.has(key)) headers.set(key, value);
  }
  const authorizationHeader = headers.get("Authorization") ?? "";
  const hasBearerAuth = authorizationHeader.startsWith("Bearer ");
  if (
    typeof window !== "undefined" &&
    needsCSRF(method) &&
    !headers.has(CSRF_HEADER_NAME) &&
    !hasBearerAuth
  ) {
    headers.set(CSRF_HEADER_NAME, await getCSRFToken(url));
  }

  // Avoid requests hanging forever (e.g. network/DNS issues). Keep this short
  // so the UI can surface an actionable error instead of showing endless spinners.
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), timeoutMs);

  let response: Response;
  try {
    response = await fetch(url, {
      ...fetchOptions,
      credentials: fetchOptions.credentials ?? "include",
      signal: fetchOptions.signal ?? controller.signal,
      headers,
    });
  } catch (err) {
    clearTimeout(timeout);
    if (err instanceof Error && err.name === "AbortError") {
      throw new Error(
        `Request timed out after ${Math.round(timeoutMs / 1000)}s: ${url}`,
      );
    }
    // Network error (CORS, offline, etc.)
    throw new Error(
      `Network error: Could not connect to API${API_BASE ? ` at ${API_BASE}` : ""}. Is the backend reachable?`,
    );
  } finally {
    clearTimeout(timeout);
  }

  if (!response.ok) {
    const error = await parseApiErrorResponse(response, {
      action: "Request failed",
      method,
    });
    // Central interceptor for the session-recovery class (401 +
    // reason_code=session_reprojection_required): silently re-enter SSO once
    // and replay the request when the session was re-minted in place.
    // Pages never interpret this class themselves.
    if (
      typeof window !== "undefined" &&
      !sessionReprojectionReplay &&
      isSessionReprojectionError(error)
    ) {
      const outcome = await recoverFromSessionReprojection(error);
      if (outcome === "retry") {
        return fetchApi<T>(endpoint, {
          ...options,
          sessionReprojectionReplay: true,
        });
      }
    }
    throw error;
  }

  const json = await response.json();
  // Back-compat: some endpoints return raw JSON instead of { data: ... }.
  if (json && typeof json === "object" && "data" in json) {
    return json as ApiResponse<T>;
  }
  return { data: json as T };
}

export async function get<T>(endpoint: string): Promise<T> {
  const result = await fetchApi<T>(endpoint, { method: "GET" });
  return result.data;
}

export async function post<T>(endpoint: string, body?: unknown): Promise<T> {
  const result = await fetchApi<T>(endpoint, {
    method: "POST",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  return result.data;
}

export async function put<T>(endpoint: string, body?: unknown): Promise<T> {
  const result = await fetchApi<T>(endpoint, {
    method: "PUT",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  return result.data;
}

export async function del<T>(endpoint: string): Promise<T> {
  const result = await fetchApi<T>(endpoint, {
    method: "DELETE",
  });
  return result.data;
}
