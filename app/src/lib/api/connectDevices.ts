/**
 * kombify Connect device read route (`GET /v1/connect/devices`, contract
 * section 9.1). The Gateway serves it with the user's own Auth0 token, the
 * same audience the Companion uses, so it is called directly and not through
 * the Techstack data plane.
 *
 * The route is still being rolled out. Every outcome is typed so the
 * dashboard can say which one it got instead of drawing an empty list:
 *   - `ready`        the device list, possibly empty
 *   - `unavailable`  the route or its auth does not exist here (404/403/503,
 *                    `connect_unavailable`, or no Gateway auth in this build)
 *   - `error`        the route exists but this read failed (network, 5xx)
 */
import { getGatewayToken } from "#lib/auth/gateway-auth.js";

import { tr } from "#lib/i18n.svelte.js";
export type ConnectAppId =
  | "workbench-desktop"
  | "workbench-mobile"
  | "companion"
  | "techstack-windows"
  | "speechkit-desktop";
export type ConnectPlatform = "windows" | "macos" | "linux" | "android" | "ios";

export interface ConnectDeviceEnvironment {
  environment_id: string;
  label: string;
  role: "owner" | "grantee";
  status: "linked" | "unlinked";
}

export interface ConnectDeviceInstallation {
  installation_id: string;
  app_id: ConnectAppId | string;
  platform: ConnectPlatform | string;
  display_name: string;
  app_version: string;
  status: "pending" | "active" | "revoked" | string;
  created_at: string;
  last_seen_at?: string;
  environments: ConnectDeviceEnvironment[];
}

export interface ConnectDevice {
  device_id: string;
  grouping: "device_ref" | "installation" | string;
  display_name: string;
  platform: ConnectPlatform | string;
  last_seen_at?: string;
  installations: ConnectDeviceInstallation[];
}

export type ConnectDeviceListResult =
  | { state: "ready"; devices: ConnectDevice[] }
  | { state: "unavailable"; reason: string }
  | { state: "error"; reason: string };

const DEFAULT_GATEWAY_ORIGIN = "https://api.kombify.io";

/** The Gateway origin this build talks to, from the Techstack data-plane base. */
export function connectGatewayOrigin(): string {
  const base = (import.meta.env.VITE_GATEWAY_API_BASE as string | undefined)
    ?.trim()
    .replace(/\/+$/, "");
  if (!base) return DEFAULT_GATEWAY_ORIGIN;
  try {
    return new URL(base).origin;
  } catch {
    return DEFAULT_GATEWAY_ORIGIN;
  }
}

function asString(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function parseInstallation(value: unknown): ConnectDeviceInstallation | null {
  if (!value || typeof value !== "object") return null;
  const raw = value as Record<string, unknown>;
  const id = asString(raw.installation_id);
  const appId = asString(raw.app_id);
  if (!id || !appId) return null;
  const environments = Array.isArray(raw.environments)
    ? raw.environments.flatMap((entry): ConnectDeviceEnvironment[] => {
        if (!entry || typeof entry !== "object") return [];
        const env = entry as Record<string, unknown>;
        const envId = asString(env.environment_id);
        if (!envId) return [];
        return [
          {
            environment_id: envId,
            label: asString(env.label) || envId,
            role: env.role === "owner" ? "owner" : "grantee",
            status: env.status === "linked" ? "linked" : "unlinked",
          },
        ];
      })
    : [];
  return {
    installation_id: id,
    app_id: appId,
    platform: asString(raw.platform),
    display_name: asString(raw.display_name) || appId,
    app_version: asString(raw.app_version),
    status: asString(raw.status) || "pending",
    created_at: asString(raw.created_at),
    last_seen_at: asString(raw.last_seen_at) || undefined,
    environments,
  };
}

/**
 * Validate the wire document at the boundary. Rows without an identity are
 * dropped, never invented; a document that is not a device list is an error.
 */
export function parseConnectDeviceList(body: unknown): ConnectDevice[] | null {
  if (!body || typeof body !== "object") return null;
  const devices = (body as { devices?: unknown }).devices;
  if (!Array.isArray(devices)) return null;
  return devices.flatMap((entry): ConnectDevice[] => {
    if (!entry || typeof entry !== "object") return [];
    const raw = entry as Record<string, unknown>;
    const id = asString(raw.device_id);
    if (!id) return [];
    const installations = Array.isArray(raw.installations)
      ? raw.installations
          .map(parseInstallation)
          .filter((item): item is ConnectDeviceInstallation => item !== null)
      : [];
    return [
      {
        device_id: id,
        grouping: asString(raw.grouping) || "installation",
        display_name: asString(raw.display_name) || id,
        platform: asString(raw.platform),
        last_seen_at: asString(raw.last_seen_at) || undefined,
        installations,
      },
    ];
  });
}

async function reasonCode(response: Response): Promise<string> {
  try {
    const body = (await response.json()) as Record<string, unknown>;
    return (
      asString(body.reason_code) ||
      asString(body.error_code) ||
      asString(body.code) ||
      asString((body.error as Record<string, unknown> | undefined)?.code)
    );
  } catch {
    return "";
  }
}

/** Classify an HTTP answer from the Connect route. */
export async function classifyConnectResponse(
  response: Response,
): Promise<ConnectDeviceListResult> {
  if (response.ok) {
    let body: unknown;
    try {
      body = await response.json();
    } catch {
      return { state: "error", reason: "invalid_response" };
    }
    const devices = parseConnectDeviceList(body);
    return devices
      ? { state: "ready", devices }
      : { state: "error", reason: "invalid_response" };
  }
  const code = await reasonCode(response);
  if (
    [403, 404, 503].includes(response.status) ||
    code === "connect_unavailable"
  ) {
    return { state: "unavailable", reason: code || `http_${response.status}` };
  }
  return { state: "error", reason: code || `http_${response.status}` };
}

export async function listConnectDevices(
  options: {
    fetchImpl?: typeof fetch;
    getToken?: () => Promise<string>;
    signal?: AbortSignal;
  } = {},
): Promise<ConnectDeviceListResult> {
  const getToken = options.getToken ?? getGatewayToken;
  const fetchImpl = options.fetchImpl ?? fetch;
  let token: string;
  try {
    token = await getToken();
  } catch {
    // A build or session without a Gateway token cannot reach Connect at all.
    return { state: "unavailable", reason: "gateway_auth_unavailable" };
  }
  let response: Response;
  try {
    response = await fetchImpl(`${connectGatewayOrigin()}/v1/connect/devices`, {
      method: "GET",
      headers: { Authorization: `Bearer ${token}`, Accept: "application/json" },
      signal: options.signal,
    });
  } catch {
    return { state: "error", reason: "network" };
  }
  return classifyConnectResponse(response);
}

const APP_LABELS: Record<string, string> = {
  get ["techstack-windows"]() {
    return tr("ui.connectDevices.techstackClient");
  },
  companion: "Companion",
  ["workbench-desktop"]: "Workbench",
  ["workbench-mobile"]: "Workbench",
  ["speechkit-desktop"]: "SpeechKit",
};

/** Human name for a kombify client, e.g. "Companion". */
export function connectAppLabel(appId: string): string {
  return APP_LABELS[appId] ?? appId.replace(/[-_]+/g, " ");
}
