import { fetchApi } from "./client";
import type { CanonicalGuardReach, CanonicalServer } from "./registry";

/**
 * kombify Guard read model as the control plane persisted it.
 *
 * Everything here is optional on purpose: a backend that predates a section
 * omits it, and an omitted section is "not evidenced", never "fine". Parsing is
 * defensive for the same reason - a malformed section is dropped instead of
 * being guessed into a state.
 */

// ---------------------------------------------------------------------------
// Read model added to `GET /api/v1/servers[/{id}]`
// ---------------------------------------------------------------------------

export type GuardSecurityState =
  "compliant" | "drifted" | "unknown" | "exception_only";

/** An approved exception, reduced to the fields a person can read. */
export interface GuardException {
  reason?: string;
  approved_by?: string;
  expires_at?: string;
}

export interface GuardFinding {
  control: string;
  state: string;
  severity: string;
  expected?: string;
  observed?: string;
  remediation?: string;
  /** Present when the deviation is an approved exception. */
  exception?: GuardException;
}

export interface GuardSecurity {
  state: GuardSecurityState;
  reason_code?: string;
  baseline_version?: string;
  observed_at?: string;
  expires_at?: string;
  findings: GuardFinding[];
}

export type GuardBackupState = "fresh" | "stale" | "unknown";
export type GuardRestoreDrillState = "verified" | "stale" | "unknown";

export interface GuardRecovery {
  backup?: {
    state: GuardBackupState;
    last_snapshot_at?: string;
    policy_max_age?: string | number;
  };
  restore_drill?: {
    state: GuardRestoreDrillState;
    verified_at?: string;
  };
}

export interface GuardUpdates {
  guard?: { version?: string; signed?: boolean; state?: string };
  os?: {
    pending_security_updates?: number;
    reboot_required?: boolean;
    last_unattended_run?: string;
  };
}

export interface GuardReadModelExtension {
  security?: GuardSecurity;
  recovery?: GuardRecovery;
  updates?: GuardUpdates;
}

// ---------------------------------------------------------------------------
// Worker capabilities (`identity_binding`, `guard_operations`, `guard_posture`)
// ---------------------------------------------------------------------------

export interface GuardOperationsEvidence {
  /** `ok` or `denied` for a Guard that journals; absent on a legacy Guard. */
  journal?: string;
  durable: boolean;
  unresolved_outcome_unknown: number;
  unacked_results: number;
  reason?: string;
}

export interface GuardPostureEvidence {
  /** `split`, `legacy_root` or `unavailable`. */
  mode: string;
  reported: boolean;
  update_state?: string;
  update_version?: string;
  update_reason?: string;
  deny_mutations: boolean;
}

export interface GuardWorkerEvidence {
  /** `host_key` (alias `key_bound`) or `legacy_bearer`. */
  identity_binding?: string;
  guard_operations?: GuardOperationsEvidence;
  guard_posture?: GuardPostureEvidence;
  /**
   * Active runtime-agent credential generation. Optional: the rotate action is
   * offered only when the backend publishes it, because the rotation is fenced
   * on the exact generation.
   */
  credential_generation?: number;
}

/**
 * The persisted Connected evidence the server read model classifies: the last
 * command the Guard answered under its current credential generation. Only
 * `verified` lets Connected pass; a heartbeat never does.
 */
export interface GuardRoundTripEvidence {
  /** `verified`, `stale`, `superseded` or `absent`. */
  state: string;
  reason_code?: string;
  acknowledged_at?: string;
}

/** Everything the seven facts are derived from, and nothing else. */
export interface GuardEvidence {
  server_id?: string;
  worker_id?: string;
  lifecycle_state?: string;
  connection?: {
    state?: string;
    reason_code?: string;
    last_heartbeat_at?: string;
  };
  reach?: CanonicalServer["reach"];
  /** Absent when the backend publishes no round-trip evidence. */
  round_trip?: GuardRoundTripEvidence;
  worker: GuardWorkerEvidence;
  extension: GuardReadModelExtension;
}

function record(value: unknown): Record<string, unknown> | null {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function text(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  return trimmed === "" ? undefined : trimmed;
}

function count(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) && value >= 0
    ? Math.floor(value)
    : undefined;
}

function flag(value: unknown): boolean | undefined {
  return typeof value === "boolean" ? value : undefined;
}

const SECURITY_STATES: readonly GuardSecurityState[] = [
  "compliant",
  "drifted",
  "unknown",
  "exception_only",
];

/**
 * The backend owns the shape of an exception. Whatever it sends is reduced to
 * the three readable fields so nothing else ever reaches the screen or a report.
 */
function parseException(value: unknown): GuardException | undefined {
  if (value === true) return {};
  const asText = text(value);
  if (asText) return { reason: asText.slice(0, 200) };
  const raw = record(value);
  if (!raw) return undefined;
  return {
    reason: text(raw.reason)?.slice(0, 200),
    approved_by: text(raw.approved_by)?.slice(0, 120),
    expires_at: text(raw.expires_at),
  };
}

function parseFinding(value: unknown): GuardFinding | null {
  const raw = record(value);
  const control = text(raw?.control);
  if (!raw || !control) return null;
  return {
    control,
    state: text(raw.state) ?? "unknown",
    severity: text(raw.severity) ?? "unknown",
    expected: text(raw.expected),
    observed: text(raw.observed),
    remediation: text(raw.remediation),
    exception: parseException(raw.exception),
  };
}

function parseSecurity(value: unknown): GuardSecurity | undefined {
  const raw = record(value);
  if (!raw) return undefined;
  const state = text(raw.state);
  const known = SECURITY_STATES.find((candidate) => candidate === state);
  return {
    // An unrecognised verdict is an unknown one; it is never upgraded.
    state: known ?? "unknown",
    reason_code: text(raw.reason_code),
    baseline_version: text(raw.baseline_version),
    observed_at: text(raw.observed_at),
    expires_at: text(raw.expires_at),
    findings: (Array.isArray(raw.findings) ? raw.findings : [])
      .map(parseFinding)
      .filter((finding): finding is GuardFinding => finding !== null),
  };
}

function parseRecovery(value: unknown): GuardRecovery | undefined {
  const raw = record(value);
  if (!raw) return undefined;
  const out: GuardRecovery = {};
  const backup = record(raw.backup);
  if (backup) {
    const state = text(backup.state);
    const policy =
      typeof backup.policy_max_age === "number"
        ? backup.policy_max_age
        : text(backup.policy_max_age);
    out.backup = {
      state: state === "fresh" || state === "stale" ? state : "unknown",
      last_snapshot_at: text(backup.last_snapshot_at),
      policy_max_age: policy,
    };
  }
  const drill = record(raw.restore_drill);
  if (drill) {
    const state = text(drill.state);
    out.restore_drill = {
      state: state === "verified" || state === "stale" ? state : "unknown",
      verified_at: text(drill.verified_at),
    };
  }
  return out.backup || out.restore_drill ? out : undefined;
}

function parseUpdates(value: unknown): GuardUpdates | undefined {
  const raw = record(value);
  if (!raw) return undefined;
  const out: GuardUpdates = {};
  const guard = record(raw.guard);
  if (guard) {
    out.guard = {
      version: text(guard.version),
      signed: flag(guard.signed),
      state: text(guard.state),
    };
  }
  const os = record(raw.os);
  if (os) {
    out.os = {
      pending_security_updates: count(os.pending_security_updates),
      reboot_required: flag(os.reboot_required),
      last_unattended_run: text(os.last_unattended_run),
    };
  }
  return out.guard || out.os ? out : undefined;
}

/** Read the Guard sections of a canonical server, dropping malformed ones. */
export function parseGuardReadModel(server: unknown): GuardReadModelExtension {
  const raw = record(server);
  if (!raw) return {};
  return {
    security: parseSecurity(raw.security),
    recovery: parseRecovery(raw.recovery),
    updates: parseUpdates(raw.updates),
  };
}

/** Read the Guard facts a worker publishes through its capability map. */
export function parseWorkerEvidence(
  capabilities: unknown,
): GuardWorkerEvidence {
  const raw = record(capabilities);
  if (!raw) return {};
  const out: GuardWorkerEvidence = {};
  const binding = text(raw.identity_binding);
  if (binding) out.identity_binding = binding;
  const operations = record(raw.guard_operations);
  if (operations) {
    out.guard_operations = {
      // The canonical server read model names the journal state `state`; the
      // worker capability map a Guard heartbeats names it `journal`.
      journal: text(operations.state) ?? text(operations.journal),
      durable: operations.durable === true,
      unresolved_outcome_unknown:
        count(operations.unresolved_outcome_unknown) ?? 0,
      unacked_results: count(operations.unacked_results) ?? 0,
      reason: text(operations.reason),
    };
  }
  const posture = record(raw.guard_posture);
  if (posture) {
    out.guard_posture = {
      mode: text(posture.mode) ?? "legacy_root",
      reported: posture.reported !== false,
      update_state: text(posture.update_state),
      update_version: text(posture.update_version),
      update_reason: text(posture.update_reason),
      deny_mutations: posture.deny_mutations === true,
    };
  }
  const generation = count(raw.credential_generation);
  if (generation !== undefined) out.credential_generation = generation;
  return out;
}

/**
 * Read the round-trip evidence of the canonical server's capabilities section.
 * Only the server read model publishes it; a worker capability map never does.
 */
export function parseRoundTripEvidence(
  capabilities: unknown,
): GuardRoundTripEvidence | undefined {
  const raw = record(record(capabilities)?.command_round_trip);
  const state = text(raw?.state);
  if (!raw || !state) return undefined;
  return {
    state: state.toLowerCase(),
    reason_code: text(raw.reason_code),
    acknowledged_at: text(raw.acknowledged_at),
  };
}

/**
 * Collect the evidence for one server. `capabilities` is the worker capability
 * map when the caller already holds it (the server detail payload carries it);
 * otherwise it is read from the canonical server, which is where a backend that
 * projects it there publishes it.
 */
export function collectGuardEvidence(
  server: CanonicalServer | null | undefined,
  capabilities?: unknown,
): GuardEvidence {
  if (!server)
    return { worker: parseWorkerEvidence(capabilities), extension: {} };
  const carried = record(server as unknown)?.capabilities;
  return {
    server_id: server.id,
    worker_id: server.worker_id,
    lifecycle_state: server.lifecycle?.state,
    connection: server.connection
      ? {
          state: server.connection.state,
          reason_code: server.connection.reason_code,
          last_heartbeat_at: server.connection.last_heartbeat_at,
        }
      : undefined,
    reach: server.reach,
    round_trip:
      parseRoundTripEvidence(carried) ?? parseRoundTripEvidence(capabilities),
    worker: parseWorkerEvidence(capabilities ?? carried),
    extension: parseGuardReadModel(server),
  };
}

// ---------------------------------------------------------------------------
// Operator APIs
// ---------------------------------------------------------------------------

export type GuardOutcomeState = "reconciling" | "needs_operator";

export interface GuardUnresolvedOutcome {
  command_id: string;
  operation: string;
  state: GuardOutcomeState | string;
  /** The committed revision an operator resolution must supersede. */
  revision: number;
  reason?: string;
  escalated: boolean;
  unresolved_since: string;
  needs_operator_at: string;
}

export type GuardOutcomeDisposition =
  "verified_applied" | "verified_not_applied" | "accept_unknown";

export interface GuardOutcomeResolution {
  command_id: string;
  revision: number;
  disposition: string;
  actor: string;
  fence_released: boolean;
}

export async function listGuardOutcomes(
  workerId: string,
): Promise<GuardUnresolvedOutcome[]> {
  const res = await fetchApi<{
    worker_id?: string;
    outcomes?: GuardUnresolvedOutcome[];
  }>(`/api/v1/workers/${encodeURIComponent(workerId)}/outcomes`);
  return res.data?.outcomes ?? [];
}

/**
 * Release the fence of one unresolved outcome. The backend demands a fresh
 * multi-factor sign-in (403, reason `outcome_resolution_step_up_required`) and
 * the revision the list returned (409, `outcome_revision_stale`).
 */
export async function resolveGuardOutcome(
  workerId: string,
  commandId: string,
  request: {
    disposition: GuardOutcomeDisposition;
    expected_revision: number;
    note: string;
  },
): Promise<GuardOutcomeResolution> {
  const res = await fetchApi<GuardOutcomeResolution>(
    `/api/v1/workers/${encodeURIComponent(workerId)}/outcomes/${encodeURIComponent(commandId)}/resolve`,
    { method: "POST", body: JSON.stringify(request) },
  );
  return res.data;
}

export interface GuardCredentialRotation {
  worker_id: string;
  server_id: string;
  credential_generation: number;
  /** Returned once for this exact attempt. Never store or log it. */
  agent_token?: string;
}

/** A fresh key per user-initiated attempt; an exact retry reuses it. */
export function newRotationKey(): string {
  const random =
    typeof crypto !== "undefined" && "randomUUID" in crypto
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `guard-rotate-${random}`;
}

export async function rotateGuardCredential(
  serverId: string,
  expectedGeneration: number,
  idempotencyKey: string,
): Promise<GuardCredentialRotation> {
  const res = await fetchApi<GuardCredentialRotation>(
    `/v1/ril/servers/${encodeURIComponent(serverId)}/credential/rotate`,
    {
      method: "POST",
      headers: { "Idempotency-Key": idempotencyKey },
      body: JSON.stringify({
        expected_credential_generation: expectedGeneration,
      }),
    },
  );
  return res.data;
}

export interface GuardPublicReachRequest {
  enabled: boolean;
  /** Required when enabling: the route label, lowercase letters, digits, hyphen. */
  service?: string;
  /** Required when enabling: the port the service listens on, on the server. */
  target_port?: number;
  target_scheme?: "http" | "https";
}

/**
 * Publish the server at a kombify.me address, or stop publishing it. Nothing is
 * published until the owner enables it with a service and port; enabling needs
 * a fresh multi-factor sign-in (403, reason `server_reach_step_up_required`).
 */
export async function setGuardPublicReach(
  serverId: string,
  request: GuardPublicReachRequest,
): Promise<CanonicalGuardReach> {
  const res = await fetchApi<CanonicalGuardReach>(
    `/api/v1/servers/${encodeURIComponent(serverId)}/reach`,
    { method: "PUT", body: JSON.stringify(request) },
  );
  return res.data;
}

export interface GuardIdentityUpgrade {
  worker_id: string;
  /** `legacy_bearer` until an owner-approved key is bound, then `host_key`. */
  identity_binding: string;
  /** `guard_identity_upgrade_required` while the server is bearer-only. */
  code?: string;
  pending?: {
    key_thumbprint: string;
    requested_at: string;
    expires_at: string;
  };
  approved_by?: string;
}

export async function getGuardIdentityUpgrade(
  workerId: string,
): Promise<GuardIdentityUpgrade> {
  const res = await fetchApi<GuardIdentityUpgrade>(
    `/api/v1/workers/${encodeURIComponent(workerId)}/identity/upgrade`,
  );
  return res.data;
}

/**
 * Approve the exact key the server proposed (fresh multi-factor sign-in
 * required, 403 reason `identity_upgrade_step_up_required`), or reject it.
 */
export async function decideGuardIdentityUpgrade(
  workerId: string,
  decision: { approve: string } | { reject: true },
): Promise<GuardIdentityUpgrade> {
  const base = `/api/v1/workers/${encodeURIComponent(workerId)}/identity/upgrade`;
  const res =
    "approve" in decision
      ? await fetchApi<GuardIdentityUpgrade>(`${base}/approve`, {
          method: "POST",
          body: JSON.stringify({ key_thumbprint: decision.approve }),
        })
      : await fetchApi<GuardIdentityUpgrade>(`${base}/reject`, {
          method: "POST",
          body: "{}",
        });
  return res.data;
}

/** Stable reason the backend attaches to an error's `details`, if any. */
export function denialReason(details: unknown): string | undefined {
  return text(record(details)?.reason);
}
