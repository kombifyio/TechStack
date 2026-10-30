/**
 * Devices and people for the dashboards. Two independent sources, each with
 * its own honest state, so one outage never renders as "nobody is here":
 *   - people: owner activation and the household list from the homelab's
 *     Pocket ID / TinyAuth, read through the Guard
 *     (`GET /api/v1/stacks/{id}/identity`)
 *   - devices: the owner's kombify clients from kombify Connect
 *     (`GET /v1/connect/devices` on the Gateway)
 */
import {
  getHomelabIdentity,
  type HomelabIdentityStatus,
} from "#lib/api/homelabIdentity.js";
import {
  connectAppLabel,
  listConnectDevices,
  type ConnectDevice,
  type ConnectDeviceListResult,
} from "#lib/api/connectDevices.js";

import { tr, trn } from "#lib/i18n.svelte.js";
import { listEdgeDevices, type EdgeDevice } from "#lib/api/edgeDevices.js";
export type Audience = "owner" | "household";

export type SectionState<T> =
  | { state: "loading" }
  | { state: "ready"; items: T[]; notices?: string[] }
  | { state: "unavailable"; message: string }
  | { state: "error"; message: string };

export interface PersonEntry {
  id: string;
  kind: "person";
  audience: Audience;
  name: string;
  detail: string;
  status: string;
  online: boolean | null;
}

export interface DeviceEntry {
  id: string;
  kind: "device";
  audience: Audience;
  name: string;
  detail: string;
  platform: string;
  clients: string[];
  lastSeen?: string;
  online: boolean | null;
  source: ConnectDevice | EdgeDevice;
  edge?: EdgeDevice;
}

export type PeopleEntry = PersonEntry | DeviceEntry;

export const connectUnavailableMessage = () =>
  tr("ui.people.connectedClientsAppearHereOnce");
export const connectErrorMessage = () => tr("ui.people.deviceListUnavailable");
export const householdUnavailableMessage = () =>
  tr("ui.people.liveStatusNotReporting");
export const identityNotReadyMessage = () => tr("ui.people.liveStatusPending");
export const noHomelabPeopleMessage = () =>
  tr("ui.people.ownerAndHouseholdAppearOnce");
export const edgeLoadErrorMessage = () => tr("box.loadError");

/** A device counts as online when it reported within this window. */
const ONLINE_WINDOW_MS = 10 * 60 * 1000;
// Edge devices check in every 5 minutes (settings polls refresh it once Gateway touches them).
const EDGE_ONLINE_WINDOW_MS = 7 * 60 * 1000;

function onlineFrom(
  lastSeen: string | undefined,
  now: number,
  windowMs = ONLINE_WINDOW_MS,
): boolean | null {
  if (!lastSeen) return null;
  const at = Date.parse(lastSeen);
  if (Number.isNaN(at)) return null;
  return now - at <= windowMs;
}

const PLATFORM_LABELS: Record<string, string> = {
  windows: "Windows",
  macos: "macOS",
  linux: "Linux",
  android: "Android",
  ios: "iOS",
  "kombify-box": "kombify Box",
  "kombify-watch": "kombify Watch",
};

export function platformLabel(platform: string): string {
  return PLATFORM_LABELS[platform] ?? platform;
}

function normalizedDeviceName(device: ConnectDevice): string {
  return `${device.display_name.trim().toLowerCase().replace(/\s+/g, " ")}|${device.platform.trim().toLowerCase()}`;
}

function newestTimestamp(
  values: Array<string | undefined>,
): string | undefined {
  let newest: string | undefined;
  let newestAt = -Infinity;
  for (const value of values) {
    const at = value ? Date.parse(value) : NaN;
    if (!Number.isNaN(at) && at > newestAt) {
      newest = value;
      newestAt = at;
    }
  }
  return newest;
}

/**
 * One row per physical device. Connect groups installations by `device_ref`;
 * an installation registered without one (Workbench dev builds) comes back as
 * its own `grouping: "installation"` device, so the same machine appears once
 * per install. Those fold by display name and platform, into the single
 * `device_ref` device of that name when exactly one exists. Two `device_ref`
 * devices are always two machines, even with the same name.
 */
export function mergeConnectDevices(
  devices: readonly ConnectDevice[],
): ConnectDevice[] {
  const groups = new Map<string, ConnectDevice[]>();
  const refByName = new Map<string, string | null>();
  const push = (key: string, device: ConnectDevice) => {
    const list = groups.get(key);
    if (list) list.push(device);
    else groups.set(key, [device]);
  };
  for (const device of devices) {
    if (device.grouping !== "device_ref") continue;
    const key = `ref:${device.device_id}`;
    push(key, device);
    const name = normalizedDeviceName(device);
    refByName.set(name, refByName.has(name) ? null : key);
  }
  for (const device of devices) {
    if (device.grouping === "device_ref") continue;
    const name = normalizedDeviceName(device);
    push(refByName.get(name) ?? `name:${name}`, device);
  }
  return [...groups.values()].map((group) => {
    const [first] = group as [ConnectDevice, ...ConnectDevice[]];
    const primary =
      group.find((device) => device.grouping === "device_ref") ?? first;
    const installations = group.flatMap((device) => device.installations);
    return {
      ...primary,
      last_seen_at: newestTimestamp([
        ...group.map((device) => device.last_seen_at),
        ...installations.map((item) => item.last_seen_at),
      ]),
      installations,
    };
  });
}

/**
 * The kombify apps on one device, one entry per app with a count for repeated
 * installs ("Workbench ×2"). Revoked installations do not count; active ones
 * sort before pending ones.
 */
export function deviceApps(device: ConnectDevice): string[] {
  const counts = new Map<string, { count: number; active: boolean }>();
  for (const installation of device.installations) {
    if (installation.status === "revoked") continue;
    const label = connectAppLabel(installation.app_id);
    const entry = counts.get(label) ?? { count: 0, active: false };
    entry.count += 1;
    entry.active ||= installation.status === "active";
    counts.set(label, entry);
  }
  return [...counts.entries()]
    .sort(([, left], [, right]) => Number(right.active) - Number(left.active))
    .map(([label, entry]) =>
      entry.count > 1 ? `${label} ×${entry.count}` : label,
    );
}

export function devicesFromConnect(
  result: ConnectDeviceListResult,
  now = Date.now(),
): SectionState<DeviceEntry> {
  if (result.state === "unavailable") {
    return { state: "unavailable", message: connectUnavailableMessage() };
  }
  if (result.state === "error") {
    return { state: "error", message: connectErrorMessage() };
  }
  return {
    state: "ready",
    items: mergeConnectDevices(result.devices).map((device) => {
      const clients = deviceApps(device);
      const appCount = trn("ui.people.appCount", clients.length);
      // Connect lists the caller's own devices (scope "subject"); a device
      // holding only grantee environments still belongs to this owner.
      return {
        id: `device:${device.device_id}`,
        kind: "device",
        audience: "owner",
        name: device.display_name,
        detail: [
          platformLabel(device.platform),
          clients.join(" · "),
          clients.length > 1 ? appCount : "",
        ]
          .filter(Boolean)
          .join(" · "),
        platform: device.platform,
        clients,
        lastSeen: device.last_seen_at,
        online: onlineFrom(device.last_seen_at, now),
        source: device,
      };
    }),
  };
}

function hostOf(origin: string | undefined): string {
  if (!origin) return "";
  try {
    return new URL(origin).host;
  } catch {
    return "";
  }
}

const OWNER_STATUS: Record<string, string> = {
  get active() {
    return tr("ui.people.signedInToTheHomelab");
  },
  get pending() {
    return tr("ui.people.activationPending");
  },
  get expired() {
    return tr("ui.people.activationExpired");
  },
  get failed() {
    return tr("ui.people.identityStatusUnavailable");
  },
  get unavailable() {
    return tr("ui.people.identityProviderNotReady");
  },
};

/**
 * Fold the per-deployment identity reads into owner + household. The owner
 * (the signed-in account) and the people planned in the Wizard are
 * Techstack's own records and always show; only live sign-in status and
 * invited members need the homelab. A read that failed, or a Guard snapshot
 * the backend marks `identity_status_failed`, adds a notice instead of
 * passing for an empty household.
 */
export function peopleFromIdentity(
  results: PromiseSettledResult<HomelabIdentityStatus>[],
  ownerName: string,
): SectionState<PersonEntry> {
  const answered = results.flatMap((result) =>
    result.status === "fulfilled" ? [result.value] : [],
  );
  const reporting = answered.filter(
    (status) =>
      !(
        status.owner.status === "failed" &&
        status.owner.reason_code === "identity_status_failed"
      ),
  );
  const withProvider = reporting.filter(
    (status) => status.owner.status !== "unavailable",
  );
  // A stack that did not report hides its invited members, also when
  // another stack reports.
  const notices = [
    ...(reporting.length < results.length
      ? [householdUnavailableMessage()]
      : []),
    ...(reporting.length > 0 && withProvider.length === 0
      ? [identityNotReadyMessage()]
      : []),
  ];
  const owner: HomelabIdentityStatus["owner"] = withProvider.find(
    (status) => status.owner.status === "active",
  )?.owner ??
    withProvider[0]?.owner ??
    reporting[0]?.owner ?? { status: "failed" };
  const people: PersonEntry[] = [
    {
      id: "person:owner",
      kind: "person",
      audience: "owner",
      name: ownerName || tr("ui.homelabDashboardPage.you"),
      detail: hostOf(owner.origin) || tr("ui.people.homelabOwner"),
      status: OWNER_STATUS[owner.status] ?? owner.status,
      online: null,
    },
  ];
  const members = new Map<string, PersonEntry>();
  for (const status of withProvider) {
    for (const member of status.household.members ?? []) {
      const key = member.username.trim().toLowerCase();
      if (!key || members.has(key)) continue;
      members.set(key, {
        id: `person:${key}`,
        kind: "person",
        audience: "household",
        name: member.display_name?.trim() || member.username,
        detail: member.email?.trim() || member.username,
        status: member.status?.trim() || tr("ui.people.member"),
        online: null,
      });
    }
  }
  // Without a live member list an invitation cannot be ruled out, so a
  // planned person is not called "not invited yet".
  const plannedStatus =
    withProvider.length > 0
      ? tr("ui.people.notInvitedYet")
      : tr("ui.people.plannedMember");
  for (const status of answered) {
    for (const planned of status.household.planned_people ?? []) {
      const key = `planned:${planned.client_ref}`;
      const invited = [...members.values()].some(
        (entry) =>
          planned.email &&
          entry.detail.toLowerCase() === planned.email.toLowerCase(),
      );
      if (invited || members.has(key)) continue;
      members.set(key, {
        id: `person:${key}`,
        kind: "person",
        audience: "household",
        name:
          planned.name?.trim() ||
          planned.email?.trim() ||
          tr("ui.people.plannedMember"),
        detail: planned.email?.trim() || tr("ui.people.plannedInTheWizard"),
        status: plannedStatus,
        online: null,
      });
    }
  }
  return { state: "ready", items: [...people, ...members.values()], notices };
}

export async function loadPeople(
  deploymentIds: readonly string[],
  ownerName: string,
): Promise<SectionState<PersonEntry>> {
  if (deploymentIds.length === 0) {
    return { state: "unavailable", message: noHomelabPeopleMessage() };
  }
  const results = await Promise.allSettled(
    deploymentIds.map((id) => getHomelabIdentity(id)),
  );
  return peopleFromIdentity(results, ownerName);
}

export async function loadDevices(): Promise<SectionState<DeviceEntry>> {
  const [clients, edges] = await Promise.all([
    listConnectDevices(),
    listEdgeDevices(),
  ]);
  const clientState = devicesFromConnect(clients);
  const edgeMessage = edges.state === "error" ? edgeLoadErrorMessage() : "";
  if (clientState.state !== "ready" && edges.state !== "ready") {
    return {
      state:
        clientState.state === "error" || edges.state === "error"
          ? "error"
          : "unavailable",
      message: [
        "message" in clientState ? clientState.message : "",
        edgeMessage,
      ]
        .filter(Boolean)
        .join(" "),
    };
  }
  const now = Date.now();
  return {
    state: "ready",
    items: [
      ...(clientState.state === "ready" ? clientState.items : []),
      ...(edges.state === "ready"
        ? edges.value.map((edge): DeviceEntry => ({
            id: `edge:${edge.device_id}`,
            kind: "device",
            audience: "owner",
            name: edge.display_name,
            detail: [edge.board, edge.firmware_version, edge.local_ip]
              .filter(Boolean)
              .join(" · "),
            platform: edge.kind,
            clients: [],
            lastSeen: edge.last_seen_at,
            online: onlineFrom(edge.last_seen_at, now, EDGE_ONLINE_WINDOW_MS),
            source: edge,
            edge,
          }))
        : []),
    ],
    notices: [
      ...(clientState.state !== "ready" && "message" in clientState
        ? [clientState.message]
        : []),
      ...(edges.state === "error" ? [edgeMessage] : []),
    ],
  };
}
