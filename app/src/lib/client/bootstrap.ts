/**
 * Client bootstrap config (ADR-033 OQ2 web convergence).
 *
 * The SPA is built once as a static bundle and served by the Go backend, so
 * runtime-varying public configuration (telemetry toggles, edition) can no
 * longer come from `$env/dynamic/public`. Instead the app fetches
 * GET /api/v1/client/bootstrap once at startup and consumers read the cached
 * snapshot. All values default to empty strings, which keeps selfhost-oss and
 * local deployments telemetry-free unless the backend explicitly provides
 * telemetry configuration.
 */

import { writable, get } from "svelte/store";

export interface SentryBootstrap {
  dsn: string;
  environment: string;
  release: string;
}

export interface PostHogBootstrap {
  key: string;
  host: string;
  environment: string;
}

export interface ClientBootstrap {
  edition: string;
  deploymentMode: string;
  kombifyEdition: string;
  version: string;
  publicOrigin: string;
  telemetry: {
    sentry: SentryBootstrap;
    posthog: PostHogBootstrap;
  };
}

export const BOOTSTRAP_ENDPOINT = "/api/v1/client/bootstrap";

export function emptyClientBootstrap(): ClientBootstrap {
  return {
    edition: "",
    deploymentMode: "",
    kombifyEdition: "",
    version: "",
    publicOrigin: "",
    telemetry: {
      sentry: { dsn: "", environment: "", release: "" },
      posthog: { key: "", host: "", environment: "" },
    },
  };
}

function asString(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object"
    ? (value as Record<string, unknown>)
    : {};
}

/**
 * Parses a bootstrap response body. Accepts both the httpx success envelope
 * ({"data": {...}}) and a bare payload; unknown or malformed fields fall back
 * to empty strings so a partial response can never turn telemetry on.
 */
export function parseClientBootstrap(body: unknown): ClientBootstrap {
  const root = asRecord(body);
  const data = asRecord("data" in root ? root.data : root);
  const telemetry = asRecord(data.telemetry);
  const sentry = asRecord(telemetry.sentry);
  const posthog = asRecord(telemetry.posthog);

  return {
    edition: asString(data.edition),
    deploymentMode: asString(data.deployment_mode),
    kombifyEdition: asString(data.kombify_edition),
    version: asString(data.version),
    publicOrigin: asString(data.public_origin),
    telemetry: {
      sentry: {
        dsn: asString(sentry.dsn),
        environment: asString(sentry.environment),
        release: asString(sentry.release),
      },
      posthog: {
        key: asString(posthog.key),
        host: asString(posthog.host),
        environment: asString(posthog.environment),
      },
    },
  };
}

export const clientBootstrap = writable<ClientBootstrap>(
  emptyClientBootstrap(),
);

let pendingLoad: Promise<ClientBootstrap> | null = null;

/**
 * Fetches the bootstrap config once and caches the result. Errors resolve to
 * the empty (telemetry-free) config instead of rejecting, so app startup never
 * blocks on telemetry configuration.
 */
export function loadClientBootstrap(
  fetchImpl: typeof fetch = fetch,
): Promise<ClientBootstrap> {
  if (pendingLoad) {
    return pendingLoad;
  }
  pendingLoad = (async () => {
    try {
      const response = await fetchImpl(BOOTSTRAP_ENDPOINT, {
        headers: { Accept: "application/json" },
      });
      if (!response.ok) {
        return emptyClientBootstrap();
      }
      const parsed = parseClientBootstrap(await response.json());
      clientBootstrap.set(parsed);
      return parsed;
    } catch {
      return emptyClientBootstrap();
    }
  })();
  return pendingLoad;
}

/** Returns the current bootstrap snapshot (empty defaults before load). */
export function getClientBootstrap(): ClientBootstrap {
  return get(clientBootstrap);
}

/** Test-only: clears the cached fetch and store state. */
export function resetClientBootstrapForTesting(): void {
  pendingLoad = null;
  clientBootstrap.set(emptyClientBootstrap());
}
