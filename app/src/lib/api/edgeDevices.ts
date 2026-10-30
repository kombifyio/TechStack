/** Gateway Connect contract §9.2. A Box is a directory device, never an installation. */
import { getGatewayToken } from "#lib/auth/gateway-auth.js";
import { connectGatewayOrigin } from "#lib/api/connectDevices.js";

export interface BoxSettings {
  schema: "speechkit.box_settings.v1";
  revision: number;
  wake?: { enabled?: boolean; phrase?: string };
  assist?: {
    max_followups?: number;
    sound_cues?: boolean;
    sound_cue_style?: "chime" | "pop" | "glass" | "bell";
    voice?: "auto" | "female" | "male";
    route?: "fast" | "agent";
  };
  audio?: { volume?: number };
  locale?: string;
  display?: { design?: "classic" | "watch" };
  firmware?: {
    channel?: "stable";
    target_version?: string | null;
    auto_update?: boolean;
    update_request?: number;
  };
}

export interface EdgeDevice {
  device_id: string;
  kind: "kombify-box" | "kombify-watch";
  board: string;
  firmware_version: string;
  display_name: string;
  local_ip?: string;
  created_at: string;
  last_seen_at: string;
  reported: Record<string, unknown>;
  settings: BoxSettings;
  sync?: {
    applied_revision: number;
    local_pending?: boolean;
    rejected_revision?: number;
    reject_reason?: string;
  };
  firmware_update?: {
    state:
      | "idle"
      | "checking"
      | "current"
      | "available"
      | "installing"
      | "rebooting"
      | "error";
    version?: string;
    detail?: string;
    progress_percent?: number;
    handled_request?: number;
    updated_at?: string;
  };
}

export type EdgeResult<T> =
  | { state: "ready"; value: T }
  | {
      state: "unavailable" | "error" | "conflict";
      reason: string;
      status?: number;
    };

export function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

const locales = ["en", "de", "es", "zh-Hans", "ar", "hi"];
export const firmwareVersionPattern =
  /^\d{1,4}\.\d{1,4}\.\d{1,6}(-[0-9A-Za-z.-]{1,20})?$/;
const integer = (value: unknown, min: number, max: number) =>
  typeof value === "number" &&
  Number.isInteger(value) &&
  value >= min &&
  value <= max;

/** Reject malformed documents before they can become a revision-protected write. */
export function parseBoxSettings(value: unknown): BoxSettings | null {
  const raw = record(value);
  if (
    !raw ||
    raw.schema !== "speechkit.box_settings.v1" ||
    !integer(raw.revision, 0, 2147483647)
  )
    return null;
  const allowed: Record<string, Record<string, (v: unknown) => boolean>> = {
    wake: {
      enabled: (v) => typeof v === "boolean",
      phrase: (v) => typeof v === "string" && /^[a-z0-9_]{1,15}$/.test(v),
    },
    assist: {
      max_followups: (v) => integer(v, 0, 3),
      sound_cues: (v) => typeof v === "boolean",
      sound_cue_style: (v) =>
        typeof v === "string" && ["chime", "pop", "glass", "bell"].includes(v),
      voice: (v) =>
        typeof v === "string" && ["auto", "female", "male"].includes(v),
      route: (v) => typeof v === "string" && ["fast", "agent"].includes(v),
    },
    audio: { volume: (v) => integer(v, 0, 100) },
    display: {
      design: (v) => typeof v === "string" && ["classic", "watch"].includes(v),
    },
    firmware: {
      channel: (v) => v === "stable",
      target_version: (v) =>
        v === null || (typeof v === "string" && firmwareVersionPattern.test(v)),
      auto_update: (v) => typeof v === "boolean",
      update_request: (v) => integer(v, 0, 2147483647),
    },
  };
  for (const [key, entry] of Object.entries(raw)) {
    if (key === "schema" || key === "revision") continue;
    if (key === "locale") {
      if (typeof entry !== "string" || !locales.includes(entry)) return null;
      continue;
    }
    // Validate known fields while accepting additive Gateway response members.
    if (!Object.hasOwn(allowed, key)) continue;
    const rules = allowed[key];
    const part = record(entry);
    if (
      !rules ||
      !part ||
      Object.entries(part).some(
        ([name, v]) => Object.hasOwn(rules, name) && !rules[name](v),
      )
    )
      return null;
  }
  return raw as unknown as BoxSettings;
}

function parseEdgeDevice(value: unknown): EdgeDevice | null {
  const raw = record(value);
  if (
    !raw ||
    (raw.kind !== "kombify-box" && raw.kind !== "kombify-watch") ||
    !record(raw.reported) ||
    !parseBoxSettings(raw.settings)
  )
    return null;
  if (
    [
      "device_id",
      "board",
      "firmware_version",
      "display_name",
      "created_at",
      "last_seen_at",
    ].some((key) => typeof raw[key] !== "string")
  )
    return null;
  if (
    !String(raw.device_id).startsWith("dev_") ||
    !Number.isFinite(Date.parse(String(raw.last_seen_at)))
  )
    return null;
  const sync =
    raw.sync === undefined ? { applied_revision: 0 } : record(raw.sync);
  const update =
    raw.firmware_update === undefined
      ? { state: "idle" }
      : record(raw.firmware_update);
  if (
    !sync ||
    !integer(sync.applied_revision, 0, 2147483647) ||
    !update ||
    typeof update.state !== "string" ||
    ![
      "idle",
      "checking",
      "current",
      "available",
      "installing",
      "rebooting",
      "error",
    ].includes(update.state)
  )
    return null;
  if (
    update.progress_percent !== undefined &&
    !integer(update.progress_percent, 0, 100)
  )
    return null;
  if (
    update.handled_request !== undefined &&
    !integer(update.handled_request, 0, 2147483647)
  )
    return null;
  if (
    sync.rejected_revision !== undefined &&
    !integer(sync.rejected_revision, 0, 2147483647)
  )
    return null;
  if (
    sync.local_pending !== undefined &&
    typeof sync.local_pending !== "boolean"
  )
    return null;
  if (
    [
      sync.reject_reason,
      update.version,
      update.detail,
      update.updated_at,
      raw.local_ip,
    ].some((v) => v !== undefined && typeof v !== "string")
  )
    return null;
  return { ...raw, sync, firmware_update: update } as unknown as EdgeDevice;
}

async function request(
  path = "",
  init: RequestInit = {},
): Promise<EdgeResult<Response>> {
  let token: string;
  try {
    token = await getGatewayToken();
  } catch {
    return { state: "unavailable", reason: "gateway_auth_unavailable" };
  }
  try {
    const response = await fetch(
      `${connectGatewayOrigin()}/v1/connect/edge-devices${path}`,
      {
        ...init,
        headers: {
          Authorization: `Bearer ${token}`,
          Accept: "application/json",
          ...init.headers,
        },
      },
    );
    if (response.ok) return { state: "ready", value: response };
    const body = record(await response.json().catch(() => null));
    return {
      status: response.status,
      state:
        response.status === 412
          ? "conflict"
          : [403, 404, 503].includes(response.status) ||
              body?.reason_code === "connect_unavailable"
            ? "unavailable"
            : "error",
      reason:
        typeof body?.reason_code === "string"
          ? body.reason_code
          : `http_${response.status}`,
    };
  } catch {
    return { state: "error", reason: "network" };
  }
}

export async function listEdgeDevices(): Promise<EdgeResult<EdgeDevice[]>> {
  const result = await request();
  if (result.state !== "ready") return result;
  const raw = record(await result.value.json().catch(() => null));
  if (raw?.scope !== "subject" || !Array.isArray(raw.edge_devices))
    return { state: "error", reason: "invalid_response" };
  const devices = raw.edge_devices.map(parseEdgeDevice);
  return devices.every((device): device is EdgeDevice => device !== null)
    ? { state: "ready", value: devices }
    : { state: "error", reason: "invalid_response" };
}

export interface SettingsRead {
  document: BoxSettings;
  etag: string;
}
async function settingsResponse(
  response: Response,
): Promise<EdgeResult<SettingsRead>> {
  const document = parseBoxSettings(await response.json().catch(() => null));
  const etag = response.headers.get("ETag");
  return document && etag === `"v${document.revision}"`
    ? { state: "ready", value: { document, etag } }
    : { state: "error", reason: "invalid_response" };
}
export async function readEdgeSettings(
  id: string,
): Promise<EdgeResult<SettingsRead>> {
  const result = await request(`/${encodeURIComponent(id)}/settings`);
  return result.state === "ready" ? settingsResponse(result.value) : result;
}
export async function writeEdgeSettings(
  id: string,
  document: BoxSettings,
  etag: string,
): Promise<EdgeResult<SettingsRead>> {
  if (!parseBoxSettings(document) || etag !== `"v${document.revision}"`)
    return { state: "error", reason: "invalid_settings" };
  const result = await request(`/${encodeURIComponent(id)}/settings`, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "If-Match": etag },
    body: JSON.stringify(document),
  });
  return result.state === "ready" ? settingsResponse(result.value) : result;
}
export async function removeEdgeDevice(id: string): Promise<EdgeResult<null>> {
  const result = await request(`/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
  return result.state === "ready" ? { state: "ready", value: null } : result;
}

/** A watch applies only these settings; it has no wake word, cues, voice, route or firmware. */
const WATCH_SETTINGS = ["assist.max_followups", "audio.volume", "locale"];

/**
 * Whether the device applies a setting path such as `assist.voice`. A watch
 * applies a fixed short list; a Box that does not report `supports` (every Box
 * firmware so far) applies them all.
 */
export function supportsSetting(device: EdgeDevice, path: string): boolean {
  if (device.kind === "kombify-watch") return WATCH_SETTINGS.includes(path);
  const supports = device.reported.supports;
  return !Array.isArray(supports) || supports.includes(path);
}

/**
 * Whether the device offers the screen design choice (`display.design`). Unlike
 * the other rows this is opt-in: a Box that does not list it in
 * `reported.supports` has no such setting, and a watch never has one.
 */
export function supportsDesign(device: EdgeDevice): boolean {
  if (device.kind !== "kombify-box") return false;
  const supports = device.reported.supports;
  return Array.isArray(supports) && supports.includes("display.design");
}

/** Options come only from this board; never widen them with a dashboard default. */
export function reportedOptions(
  device: EdgeDevice,
  section: string,
  key: string,
  allowed?: readonly string[],
): string[] {
  const values = section
    ? record(device.reported[section])?.[key]
    : device.reported[key];
  return Array.isArray(values)
    ? [
        ...new Set(
          values.filter(
            (v): v is string =>
              typeof v === "string" && (!allowed || allowed.includes(v)),
          ),
        ),
      ]
    : [];
}
