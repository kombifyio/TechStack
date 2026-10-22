/**
 * The seven kombify Guard facts and the pairing readiness ladder, derived from
 * the persisted read model.
 *
 * Rules that are the whole point of this module:
 *  - A fact is `ok` only when it is positively evidenced by fresh evidence.
 *  - Evidence that is absent, malformed or older than its freshness bound is
 *    `unknown`, never `ok`. A heartbeat alone establishes nothing: Connected
 *    also needs the persisted authenticated command/result round trip that
 *    the server read model classifies.
 *  - A legacy credential, journal or privilege layout is reported as reduced
 *    protection (`attention`), never as fine.
 *  - "Guard-ready" needs all seven facts `ok`.
 *
 * The module is pure: no i18n, no I/O. It returns message ids and parameters;
 * components translate them. Freshness is evaluated against a caller-supplied
 * `now` so the panel can age evidence on screen without refetching.
 */

import type { GuardEvidence } from "#lib/api/guard.js";

export type GuardFactKey =
  | "bound"
  | "connected"
  | "operations"
  | "configuration"
  | "updated"
  | "recoverable"
  | "reachable";

export const GUARD_FACT_KEYS: readonly GuardFactKey[] = [
  "bound",
  "connected",
  "operations",
  "configuration",
  "updated",
  "recoverable",
  "reachable",
];

/** `ok` is positively evidenced; `attention` is a known problem; `unknown` is neither. */
export type GuardFactState = "ok" | "attention" | "unknown";

export interface GuardMessage {
  id: string;
  params?: Record<string, string | number>;
}

export interface GuardFact {
  key: GuardFactKey;
  state: GuardFactState;
  /** Legacy credential, journal or privilege layout: reduced protection. */
  reduced: boolean;
  /** When the evidence behind this fact was observed, if it carries a time. */
  observedAt?: string;
  /** Why the fact has this state. */
  reason: GuardMessage;
  /** What the owner can do next; absent when nothing is needed. */
  next?: GuardMessage;
  /** Additional evidenced details (baseline version, counts, address). */
  details: GuardMessage[];
}

export type GuardOverall = "ready" | "attention" | "unverified";

export interface GuardAssessment {
  facts: GuardFact[];
  okCount: number;
  attentionCount: number;
  unknownCount: number;
  /** True only when every one of the seven facts is positively evidenced. */
  ready: boolean;
  overall: GuardOverall;
}

/**
 * Freshness bounds. They apply only where the read model carries no expiry of
 * its own; an `expires_at` from the backend always wins.
 */
export const GUARD_FRESHNESS = {
  /** Heartbeat-derived facts (bound, connected, operations, posture). */
  heartbeatMs: 5 * 60_000,
  /** Host-security and update evidence without an explicit `expires_at`. */
  observationMs: 24 * 60 * 60_000,
  /** Reach registration evidence. */
  reachMs: 24 * 60 * 60_000,
} as const;

export type Freshness = "fresh" | "stale" | "none";

function parseTime(value: string | undefined): number | undefined {
  if (!value) return undefined;
  const at = Date.parse(value);
  return Number.isNaN(at) ? undefined : at;
}

/**
 * Freshness of one observation. `none` means there is no usable time at all,
 * which callers treat exactly like `stale`: nothing is evidenced.
 */
export function freshnessOf(
  observedAt: string | undefined,
  maxAgeMs: number,
  now: number,
  expiresAt?: string,
): Freshness {
  const expires = parseTime(expiresAt);
  if (expires !== undefined && expires <= now) return "stale";
  const observed = parseTime(observedAt);
  if (observed === undefined) return expires !== undefined ? "fresh" : "none";
  // An observation from the future cannot be trusted as current evidence.
  if (observed - now > 60_000) return "none";
  return now - observed <= maxAgeMs ? "fresh" : "stale";
}

/**
 * Parse a policy age: seconds as a number, `"90"` seconds, a Go duration such
 * as `"24h"` or `"1h30m"`, or an ISO 8601 duration such as `"PT24H"` / `"P2D"`.
 * Returns undefined for anything else rather than guessing.
 */
export function parsePolicyAgeMs(
  value: string | number | undefined,
): number | undefined {
  if (value === undefined) return undefined;
  if (typeof value === "number") {
    return Number.isFinite(value) && value > 0 ? value * 1000 : undefined;
  }
  const trimmed = value.trim();
  if (/^\d+$/.test(trimmed)) return Number(trimmed) * 1000;
  const iso = /^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$/i.exec(
    trimmed,
  );
  if (iso && trimmed.length > 1 && !/^PT?$/i.test(trimmed)) {
    const [, d, h, m, s] = iso;
    const total =
      Number(d ?? 0) * 86_400 +
      Number(h ?? 0) * 3_600 +
      Number(m ?? 0) * 60 +
      Number(s ?? 0);
    return total > 0 ? total * 1000 : undefined;
  }
  const go = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/.exec(trimmed);
  if (go && trimmed !== "") {
    const [, h, m, s] = go;
    const total = Number(h ?? 0) * 3_600 + Number(m ?? 0) * 60 + Number(s ?? 0);
    return total > 0 ? total * 1000 : undefined;
  }
  return undefined;
}

function msg(
  id: string,
  params?: Record<string, string | number>,
): GuardMessage {
  return params ? { id, params } : { id };
}

function fact(
  key: GuardFactKey,
  state: GuardFactState,
  reason: GuardMessage,
  extra: Partial<Omit<GuardFact, "key" | "state" | "reason">> = {},
): GuardFact {
  return {
    key,
    state,
    reduced: false,
    details: [],
    reason,
    ...extra,
  };
}

function normalized(value: string | undefined): string {
  return (value ?? "").trim().toLowerCase();
}

// ---------------------------------------------------------------------------
// Heartbeat freshness shared by every heartbeat-derived fact
// ---------------------------------------------------------------------------

function heartbeatFreshness(evidence: GuardEvidence, now: number): Freshness {
  return freshnessOf(
    evidence.connection?.last_heartbeat_at,
    GUARD_FRESHNESS.heartbeatMs,
    now,
  );
}

function isRevoked(evidence: GuardEvidence): boolean {
  return normalized(evidence.connection?.state) === "revoked";
}

// ---------------------------------------------------------------------------
// The seven facts
// ---------------------------------------------------------------------------

function boundFact(evidence: GuardEvidence, now: number): GuardFact {
  const observedAt = evidence.connection?.last_heartbeat_at;
  if (isRevoked(evidence)) {
    return fact("bound", "attention", msg("guard.fact.bound.revoked"), {
      observedAt,
      next: msg("guard.fact.bound.revokedNext"),
    });
  }
  const binding = normalized(evidence.worker.identity_binding);
  if (!binding) {
    return fact("bound", "unknown", msg("guard.fact.bound.unknown"), {
      next: msg("guard.fact.bound.unknownNext"),
    });
  }
  if (heartbeatFreshness(evidence, now) !== "fresh") {
    return fact("bound", "unknown", msg("guard.fact.bound.stale"), {
      observedAt,
      next: msg("guard.fact.connected.reconnectNext"),
    });
  }
  if (binding === "host_key" || binding === "key_bound") {
    return fact("bound", "ok", msg("guard.fact.bound.ok"), { observedAt });
  }
  if (binding === "legacy_bearer") {
    return fact("bound", "attention", msg("guard.fact.bound.legacy"), {
      observedAt,
      reduced: true,
      next: msg("guard.fact.bound.legacyNext"),
    });
  }
  return fact("bound", "unknown", msg("guard.fact.bound.unknown"), {
    observedAt,
    next: msg("guard.fact.bound.unknownNext"),
  });
}

function connectedFact(evidence: GuardEvidence, now: number): GuardFact {
  const connection = evidence.connection;
  const observedAt = connection?.last_heartbeat_at;
  const code = connection?.reason_code;
  const details = code ? [msg("guard.detail.reasonCode", { code })] : [];
  switch (normalized(connection?.state)) {
    case "connected":
      if (heartbeatFreshness(evidence, now) !== "fresh") {
        return fact("connected", "unknown", msg("guard.fact.connected.stale"), {
          observedAt,
          details,
          next: msg("guard.fact.connected.reconnectNext"),
        });
      }
      // A fresh heartbeat proves authentication only. Connected also needs
      // the persisted round trip the server read model classified.
      switch (normalized(evidence.round_trip?.state)) {
        case "verified":
          return fact("connected", "ok", msg("guard.fact.connected.ok"), {
            observedAt: evidence.round_trip?.acknowledged_at ?? observedAt,
            details,
          });
        case "stale":
        case "superseded":
          return fact(
            "connected",
            "unknown",
            msg("guard.fact.connected.roundTripStale"),
            {
              observedAt: evidence.round_trip?.acknowledged_at ?? observedAt,
              details,
              next: msg("guard.fact.connected.roundTripNext"),
            },
          );
        default:
          return fact(
            "connected",
            "unknown",
            msg("guard.fact.connected.awaitingRoundTrip"),
            {
              observedAt,
              details,
              next: msg("guard.fact.connected.roundTripNext"),
            },
          );
      }
    case "degraded":
      return fact(
        "connected",
        "attention",
        msg("guard.fact.connected.degraded"),
        {
          observedAt,
          details,
          next: msg("guard.fact.connected.reconnectNext"),
        },
      );
    case "offline":
      return fact(
        "connected",
        "attention",
        msg("guard.fact.connected.offline"),
        {
          observedAt,
          details,
          next: msg("guard.fact.connected.reconnectNext"),
        },
      );
    case "revoked":
      return fact(
        "connected",
        "attention",
        msg("guard.fact.connected.revoked"),
        {
          observedAt,
          details,
          next: msg("guard.fact.bound.revokedNext"),
        },
      );
    case "stale":
      return fact("connected", "unknown", msg("guard.fact.connected.stale"), {
        observedAt,
        details,
        next: msg("guard.fact.connected.reconnectNext"),
      });
    case "pending":
    case "connecting":
      return fact("connected", "unknown", msg("guard.fact.connected.pending"), {
        observedAt,
        details,
        next: msg("guard.fact.connected.pendingNext"),
      });
    default:
      return fact("connected", "unknown", msg("guard.fact.connected.unknown"), {
        next: msg("guard.fact.connected.pendingNext"),
      });
  }
}

function operationsFact(evidence: GuardEvidence, now: number): GuardFact {
  const ops = evidence.worker.guard_operations;
  const observedAt = evidence.connection?.last_heartbeat_at;
  if (!ops) {
    return fact("operations", "unknown", msg("guard.fact.operations.unknown"), {
      next: msg("guard.fact.operations.unknownNext"),
    });
  }
  if (heartbeatFreshness(evidence, now) !== "fresh") {
    return fact("operations", "unknown", msg("guard.fact.operations.stale"), {
      observedAt,
      next: msg("guard.fact.connected.reconnectNext"),
    });
  }
  if (ops.unresolved_outcome_unknown > 0) {
    return fact(
      "operations",
      "attention",
      msg("guard.fact.operations.unresolved", {
        count: ops.unresolved_outcome_unknown,
      }),
      {
        observedAt,
        next: msg("guard.fact.operations.unresolvedNext"),
      },
    );
  }
  if (!ops.journal) {
    // A Guard that reports no journal state predates the durable journal.
    return fact(
      "operations",
      "attention",
      msg("guard.fact.operations.legacy"),
      {
        observedAt,
        reduced: true,
        next: msg("guard.fact.operations.legacyNext"),
      },
    );
  }
  if (ops.journal !== "ok" || !ops.durable) {
    return fact(
      "operations",
      "attention",
      msg("guard.fact.operations.denied"),
      {
        observedAt,
        details: ops.reason
          ? [msg("guard.detail.reasonCode", { code: ops.reason })]
          : [],
        next: msg("guard.fact.operations.deniedNext"),
      },
    );
  }
  return fact("operations", "ok", msg("guard.fact.operations.ok"), {
    observedAt,
    details:
      ops.unacked_results > 0
        ? [msg("guard.fact.operations.unacked", { count: ops.unacked_results })]
        : [],
  });
}

function configurationFact(evidence: GuardEvidence, now: number): GuardFact {
  const security = evidence.extension.security;
  if (!security) {
    return fact(
      "configuration",
      "unknown",
      msg("guard.fact.configuration.notReported"),
      {
        next: msg("guard.fact.configuration.notReportedNext"),
      },
    );
  }
  const details: GuardMessage[] = [];
  if (security.baseline_version) {
    details.push(
      msg("guard.fact.configuration.baseline", {
        version: security.baseline_version,
      }),
    );
  }
  const observedAt = security.observed_at;
  if (security.state === "unknown") {
    if (security.reason_code) {
      details.push(
        msg("guard.detail.reasonCode", { code: security.reason_code }),
      );
    }
    return fact(
      "configuration",
      "unknown",
      msg("guard.fact.configuration.unknown"),
      {
        observedAt,
        details,
        next: msg("guard.fact.configuration.verifyNext"),
      },
    );
  }
  if (
    freshnessOf(
      observedAt,
      GUARD_FRESHNESS.observationMs,
      now,
      security.expires_at,
    ) !== "fresh"
  ) {
    return fact(
      "configuration",
      "unknown",
      msg("guard.fact.configuration.stale"),
      {
        observedAt,
        details,
        next: msg("guard.fact.configuration.verifyNext"),
      },
    );
  }
  if (security.state === "drifted") {
    const drifted = security.findings.filter(
      (finding) => normalized(finding.state) === "drifted",
    ).length;
    return fact(
      "configuration",
      "attention",
      msg("guard.fact.configuration.drifted", {
        count: drifted,
      }),
      {
        observedAt,
        details,
        next: msg("guard.fact.configuration.driftedNext"),
      },
    );
  }
  if (security.state === "exception_only") {
    const exceptions = security.findings.filter(
      (finding) => finding.exception !== undefined,
    ).length;
    details.push(
      msg("guard.fact.configuration.exceptions", { count: exceptions }),
    );
  }
  return fact(
    "configuration",
    "ok",
    msg(
      security.state === "exception_only"
        ? "guard.fact.configuration.okExceptions"
        : "guard.fact.configuration.ok",
    ),
    { observedAt, details },
  );
}

const UPDATE_OK_STATES = new Set([
  "current",
  "up_to_date",
  "committed",
  "update_committed",
  "ok",
]);
const UPDATE_PROBLEM_STATES = new Set([
  "outdated",
  "below_minimum",
  "unsigned",
  "failed",
  "update_denied",
  "update_rolled_back",
  "rolled_back",
  "recovery-needed",
  "recovery_needed",
]);
const UPDATE_PENDING_STATES = new Set(["update_staged", "update_trial"]);

type Half = {
  state: GuardFactState;
  reason: GuardMessage;
  next?: GuardMessage;
};

function guardUpdateHalf(evidence: GuardEvidence): Half {
  const guard = evidence.extension.updates?.guard;
  const posture = evidence.worker.guard_posture;
  const postureState = normalized(posture?.update_state);
  const version = guard?.version ?? posture?.update_version;

  // The Guard's own report of its latest update outranks a static status.
  if (UPDATE_PROBLEM_STATES.has(postureState)) {
    return {
      state: "attention",
      reason: msg("guard.fact.updated.guardProblem", {
        state: postureState,
        version: version ?? "—",
      }),
      next: msg("guard.fact.updated.guardProblemNext"),
    };
  }
  if (UPDATE_PENDING_STATES.has(postureState)) {
    return {
      state: "unknown",
      reason: msg("guard.fact.updated.guardInProgress"),
    };
  }
  if (!guard) {
    return {
      state: "unknown",
      reason: msg("guard.fact.updated.guardUnknown"),
      next: msg("guard.fact.updated.unknownNext"),
    };
  }
  if (guard.signed === false) {
    return {
      state: "attention",
      reason: msg("guard.fact.updated.guardUnsigned"),
      next: msg("guard.fact.updated.guardProblemNext"),
    };
  }
  const state = normalized(guard.state);
  if (UPDATE_PROBLEM_STATES.has(state)) {
    return {
      state: "attention",
      reason: msg("guard.fact.updated.guardProblem", {
        state,
        version: version ?? "—",
      }),
      next: msg("guard.fact.updated.guardProblemNext"),
    };
  }
  if (guard.signed === true && UPDATE_OK_STATES.has(state)) {
    return {
      state: "ok",
      reason: msg("guard.fact.updated.guardOk", { version: version ?? "—" }),
    };
  }
  return {
    state: "unknown",
    reason: msg("guard.fact.updated.guardUnknown"),
    next: msg("guard.fact.updated.unknownNext"),
  };
}

function osUpdateHalf(evidence: GuardEvidence): Half {
  const os = evidence.extension.updates?.os;
  if (!os) {
    return {
      state: "unknown",
      reason: msg("guard.fact.updated.osUnknown"),
      next: msg("guard.fact.updated.unknownNext"),
    };
  }
  if ((os.pending_security_updates ?? 0) > 0) {
    return {
      state: "attention",
      reason: msg("guard.fact.updated.osPending", {
        count: os.pending_security_updates ?? 0,
      }),
      next: msg("guard.fact.updated.osPendingNext"),
    };
  }
  if (os.reboot_required === true) {
    return {
      state: "attention",
      reason: msg("guard.fact.updated.osReboot"),
      next: msg("guard.fact.updated.osRebootNext"),
    };
  }
  if (os.pending_security_updates === 0 && os.reboot_required === false) {
    return { state: "ok", reason: msg("guard.fact.updated.osOk") };
  }
  return {
    state: "unknown",
    reason: msg("guard.fact.updated.osUnknown"),
    next: msg("guard.fact.updated.unknownNext"),
  };
}

function updatedFact(evidence: GuardEvidence, now: number): GuardFact {
  const observedAt = evidence.connection?.last_heartbeat_at;
  const lastRun = evidence.extension.updates?.os?.last_unattended_run;
  const halves = [guardUpdateHalf(evidence), osUpdateHalf(evidence)];
  const details = halves.map((half) => half.reason);
  if (lastRun) details.push(msg("guard.fact.updated.lastRun", { at: lastRun }));
  const worst: GuardFactState = halves.some(
    (half) => half.state === "attention",
  )
    ? "attention"
    : halves.every((half) => half.state === "ok")
      ? "ok"
      : "unknown";
  // The Guard half arrives by heartbeat: it cannot be current without one.
  const stale = heartbeatFreshness(evidence, now) !== "fresh";
  const state: GuardFactState = worst === "ok" && stale ? "unknown" : worst;
  const lead =
    state === "ok"
      ? msg("guard.fact.updated.ok")
      : state === "attention"
        ? msg("guard.fact.updated.attention")
        : msg(
            worst === "ok" && stale
              ? "guard.fact.updated.stale"
              : "guard.fact.updated.unknown",
          );
  return fact("updated", state, lead, {
    observedAt,
    details,
    next: halves.find((half) => half.state !== "ok" && half.next)?.next,
  });
}

function recoverableFact(evidence: GuardEvidence, now: number): GuardFact {
  const recovery = evidence.extension.recovery;
  if (!recovery || (!recovery.backup && !recovery.restore_drill)) {
    return fact(
      "recoverable",
      "unknown",
      msg("guard.fact.recoverable.notReported"),
      {
        next: msg("guard.fact.recoverable.notReportedNext"),
      },
    );
  }
  const backup = recovery.backup;
  const drill = recovery.restore_drill;
  const details: GuardMessage[] = [];

  // Backup: the backend's verdict, downgraded when the snapshot has since aged
  // past the policy the backend itself reported.
  let backupState: GuardFactState = "unknown";
  if (backup) {
    const policyMs = parsePolicyAgeMs(backup.policy_max_age);
    const snapshotAt = parseTime(backup.last_snapshot_at);
    const agedOut =
      policyMs !== undefined &&
      snapshotAt !== undefined &&
      now - snapshotAt > policyMs;
    if (backup.state === "stale" || (backup.state === "fresh" && agedOut)) {
      backupState = "attention";
      details.push(msg("guard.fact.recoverable.backupStale"));
    } else if (backup.state === "fresh" && snapshotAt !== undefined) {
      backupState = "ok";
      details.push(msg("guard.fact.recoverable.backupFresh"));
    } else {
      details.push(msg("guard.fact.recoverable.backupUnknown"));
    }
  } else {
    details.push(msg("guard.fact.recoverable.backupUnknown"));
  }

  let drillState: GuardFactState = "unknown";
  if (drill) {
    if (drill.state === "stale") {
      drillState = "attention";
      details.push(msg("guard.fact.recoverable.drillStale"));
    } else if (
      drill.state === "verified" &&
      parseTime(drill.verified_at) !== undefined
    ) {
      drillState = "ok";
      details.push(msg("guard.fact.recoverable.drillVerified"));
    } else {
      details.push(msg("guard.fact.recoverable.drillUnknown"));
    }
  } else {
    details.push(msg("guard.fact.recoverable.drillUnknown"));
  }

  const state: GuardFactState =
    backupState === "attention" || drillState === "attention"
      ? "attention"
      : backupState === "ok" && drillState === "ok"
        ? "ok"
        : "unknown";
  const lead =
    state === "ok"
      ? msg("guard.fact.recoverable.ok")
      : state === "attention"
        ? msg("guard.fact.recoverable.attention")
        : msg("guard.fact.recoverable.unknown");
  const observedAt = [backup?.last_snapshot_at, drill?.verified_at]
    .filter((value): value is string => Boolean(value))
    .sort((a, b) => (Date.parse(a) || 0) - (Date.parse(b) || 0))[0];
  return fact("recoverable", state, lead, {
    observedAt,
    details,
    next:
      state === "ok"
        ? undefined
        : msg(
            state === "attention"
              ? "guard.fact.recoverable.attentionNext"
              : "guard.fact.recoverable.unknownNext",
          ),
  });
}

const REACH_REASON_PREFIX = "guard.reach.reason.";

/** Reason codes the control plane records on a reach target, each with guidance. */
export const GUARD_REACH_REASON_CODES: ReadonlySet<string> = new Set([
  "guard_key_not_bound",
  "connect_tenant_unmappable",
  "host_proof_pending",
  "host_proof_rejected",
  "kombify_me_unavailable",
  "kombify_me_denied",
  "connect_unavailable",
  "connect_denied",
  "reach_layout_unsupported",
  "revocation_incomplete",
  "identity_unresolved",
]);

function reachReasonMessage(code: string | undefined): GuardMessage[] {
  return code ? [msg("guard.detail.reasonCode", { code })] : [];
}

function reachableFact(evidence: GuardEvidence, now: number): GuardFact {
  const reach = evidence.reach;
  if (!reach) {
    return fact(
      "reachable",
      "unknown",
      msg("guard.fact.reachable.notReported"),
      {
        next: msg("guard.fact.reachable.notReportedNext"),
      },
    );
  }
  const observedAt = reach.updated_at;
  const details: GuardMessage[] = [];
  if (reach.kombify_me?.address) {
    details.push(
      msg("guard.fact.reachable.address", {
        address: reach.kombify_me.address,
      }),
    );
  }
  const failedTarget = [reach.kombify_me, reach.connect].find(
    (target) => target?.state === "failed" || target?.state === "revoked",
  );
  const codes = [
    reach.kombify_me?.reason_code,
    reach.connect?.reason_code,
    reach.relay?.reason_code,
  ];
  const code = failedTarget?.reason_code ?? codes.find(Boolean);
  const reasonDetails = reachReasonMessage(code);
  const reasonNext =
    code && GUARD_REACH_REASON_CODES.has(code)
      ? msg(`${REACH_REASON_PREFIX}${code}`)
      : undefined;

  switch (reach.state) {
    case "ready": {
      if (freshnessOf(observedAt, GUARD_FRESHNESS.reachMs, now) === "stale") {
        return fact("reachable", "unknown", msg("guard.fact.reachable.stale"), {
          observedAt,
          details,
          next: msg("guard.fact.reachable.pendingNext"),
        });
      }
      if (reach.relay?.state === "denied" || reach.relay?.state === "stopped") {
        return fact(
          "reachable",
          "attention",
          msg("guard.fact.reachable.relayDown"),
          {
            observedAt,
            details: [
              ...details,
              ...reachReasonMessage(reach.relay.reason_code),
            ],
            next: msg("guard.fact.reachable.relayDownNext"),
          },
        );
      }
      if (reach.relay?.state === "connecting") {
        return fact(
          "reachable",
          "unknown",
          msg("guard.fact.reachable.relayConnecting"),
          {
            observedAt,
            details,
            next: msg("guard.fact.reachable.pendingNext"),
          },
        );
      }
      return fact("reachable", "ok", msg("guard.fact.reachable.ok"), {
        observedAt,
        details,
      });
    }
    case "failed":
      return fact(
        "reachable",
        "attention",
        msg("guard.fact.reachable.failed"),
        {
          observedAt,
          details: [...details, ...reasonDetails],
          next: reasonNext ?? msg("guard.fact.reachable.failedNext"),
        },
      );
    case "revoked":
      return fact(
        "reachable",
        "attention",
        msg("guard.fact.reachable.revoked"),
        {
          observedAt,
          details: [...details, ...reasonDetails],
          next: msg("guard.fact.reachable.revokedNext"),
        },
      );
    case "not_enabled":
      // A deliberate owner choice (nothing is published until it is enabled),
      // not a missing or failed registration. It never opts out of the
      // governed route: while Connect applies to the server, it must be
      // registered before the private server reads as reachable and governed.
      if (
        reach.connect?.applicability === "applicable" &&
        reach.connect.state !== "registered"
      ) {
        return reach.connect.state === "failed"
          ? fact("reachable", "attention", msg("guard.fact.reachable.failed"), {
              observedAt,
              details: [...details, ...reasonDetails],
              next: reasonNext ?? msg("guard.fact.reachable.failedNext"),
            })
          : fact("reachable", "unknown", msg("guard.fact.reachable.pending"), {
              observedAt,
              details: [...details, ...reasonDetails],
              next: reasonNext ?? msg("guard.fact.reachable.pendingNext"),
            });
      }
      return fact("reachable", "ok", msg("guard.fact.reachable.notEnabled"), {
        observedAt,
        next: msg("guard.fact.reachable.notEnabledNext"),
      });
    case "pending":
      return fact("reachable", "unknown", msg("guard.fact.reachable.pending"), {
        observedAt,
        details: [...details, ...reasonDetails],
        next: reasonNext ?? msg("guard.fact.reachable.pendingNext"),
      });
    default:
      return fact(
        "reachable",
        "unknown",
        msg("guard.fact.reachable.notEnrolled"),
        {
          observedAt,
          details,
          next: msg("guard.fact.reachable.notReportedNext"),
        },
      );
  }
}

/** Derive all seven facts and the overall verdict. */
export function assessGuard(
  evidence: GuardEvidence,
  now: number,
): GuardAssessment {
  const facts: GuardFact[] = [
    boundFact(evidence, now),
    connectedFact(evidence, now),
    operationsFact(evidence, now),
    configurationFact(evidence, now),
    updatedFact(evidence, now),
    recoverableFact(evidence, now),
    reachableFact(evidence, now),
  ];
  const okCount = facts.filter((entry) => entry.state === "ok").length;
  const attentionCount = facts.filter(
    (entry) => entry.state === "attention",
  ).length;
  const unknownCount = facts.length - okCount - attentionCount;
  const ready = okCount === GUARD_FACT_KEYS.length;
  return {
    facts,
    okCount,
    attentionCount,
    unknownCount,
    ready,
    overall: ready ? "ready" : attentionCount > 0 ? "attention" : "unverified",
  };
}

// ---------------------------------------------------------------------------
// Reduced-protection notices that are not one of the seven facts
// ---------------------------------------------------------------------------

/**
 * How the Guard is isolated and whether a failed update left it unable to
 * change anything. A Guard that reports no posture predates the privilege
 * split and is described as running entirely as root.
 */
export function protectionNotices(evidence: GuardEvidence): GuardMessage[] {
  const posture = evidence.worker.guard_posture;
  if (!posture) return [];
  const notices: GuardMessage[] = [];
  if (posture.mode === "legacy_root") {
    notices.push(msg("guard.notice.legacyRoot"));
  } else if (posture.mode === "unavailable") {
    notices.push(msg("guard.notice.executorUnavailable"));
  }
  if (posture.deny_mutations) notices.push(msg("guard.notice.mutationsDenied"));
  return notices;
}

// ---------------------------------------------------------------------------
// Pairing readiness ladder (add-server flow)
// ---------------------------------------------------------------------------

export type LadderStepKey =
  "bound" | "connected" | "configuration" | "reachable";
export const LADDER_STEP_KEYS: readonly LadderStepKey[] = [
  "bound",
  "connected",
  "configuration",
  "reachable",
];

/** `done` evidenced; `failed` known problem; `waiting` is the step in progress; `blocked` waits on an earlier step. */
export type LadderStepState = "done" | "failed" | "waiting" | "blocked";

export interface LadderStep {
  key: LadderStepKey;
  state: LadderStepState;
  fact: GuardFact;
}

export interface PairingLadder {
  steps: LadderStep[];
  /** All four steps evidenced. */
  complete: boolean;
}

/**
 * The ladder is a view over the same facts, in pairing order. A step after an
 * unfinished one is `blocked`, so the owner always sees exactly one place to
 * look: the first step that is not done.
 */
export function pairingLadder(assessment: GuardAssessment): PairingLadder {
  let frontier = false;
  const steps = LADDER_STEP_KEYS.map((key): LadderStep => {
    const source = assessment.facts.find((entry) => entry.key === key)!;
    let state: LadderStepState;
    if (source.state === "ok") {
      state = "done";
    } else if (frontier) {
      state = "blocked";
    } else {
      state = source.state === "attention" ? "failed" : "waiting";
      frontier = true;
    }
    return { key, state, fact: source };
  });
  return { steps, complete: steps.every((step) => step.state === "done") };
}
