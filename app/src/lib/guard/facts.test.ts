import { describe, expect, it } from "vitest";

import { collectGuardEvidence, type GuardEvidence } from "#lib/api/guard.js";
import type { CanonicalServer } from "#lib/api/registry.js";
import { assessGuard, pairingLadder, parsePolicyAgeMs } from "./facts";

const NOW = Date.parse("2026-09-30T12:00:00Z");
const minutesAgo = (minutes: number) =>
  new Date(NOW - minutes * 60_000).toISOString();
const hoursAgo = (hours: number) => minutesAgo(hours * 60);

/** A server whose seven facts are all positively and freshly evidenced. */
function readyEvidence(): GuardEvidence {
  return {
    server_id: "srv-1",
    worker_id: "wrk-1",
    connection: { state: "connected", last_heartbeat_at: minutesAgo(1) },
    reach: {
      state: "ready",
      kombify_me: { state: "registered", address: "home.kombify.me" },
      connect: { state: "registered", node_id: "node-1" },
      relay: { state: "connected" },
      updated_at: minutesAgo(10),
    },
    worker: {
      identity_binding: "host_key",
      guard_operations: {
        journal: "ok",
        durable: true,
        unresolved_outcome_unknown: 0,
        unacked_results: 0,
      },
      guard_posture: { mode: "split", reported: true, deny_mutations: false },
    },
    extension: {
      security: {
        state: "compliant",
        baseline_version: "2026.09",
        observed_at: minutesAgo(30),
        expires_at: new Date(NOW + 3_600_000).toISOString(),
        findings: [],
      },
      recovery: {
        backup: {
          state: "fresh",
          last_snapshot_at: hoursAgo(2),
          policy_max_age: "24h",
        },
        restore_drill: { state: "verified", verified_at: hoursAgo(48) },
      },
      updates: {
        guard: { version: "0.34.0", signed: true, state: "current" },
        os: { pending_security_updates: 0, reboot_required: false },
      },
    },
  };
}

describe("Guard-ready", () => {
  it("is claimed only when all seven facts are positively evidenced", () => {
    expect(assessGuard(readyEvidence(), NOW).ready).toBe(true);

    // Removing the evidence behind any single fact withdraws the claim, and
    // only that fact: the others stay evidenced.
    const withdrawals: Array<[string, (e: GuardEvidence) => void]> = [
      ["bound", (e) => delete e.worker.identity_binding],
      [
        "connected",
        (e) =>
          (e.connection = { state: "stale", last_heartbeat_at: minutesAgo(1) }),
      ],
      ["operations", (e) => delete e.worker.guard_operations],
      ["configuration", (e) => delete e.extension.security],
      ["updated", (e) => delete e.extension.updates],
      ["recoverable", (e) => delete e.extension.recovery],
      ["reachable", (e) => delete e.reach],
    ];
    for (const [key, withdraw] of withdrawals) {
      const evidence = readyEvidence();
      withdraw(evidence);
      const { facts, ready } = assessGuard(evidence, NOW);
      expect(ready).toBe(false);
      for (const fact of facts) {
        // Missing evidence is unknown, never a reassuring or an alarming state.
        expect(fact.state).toBe(fact.key === key ? "unknown" : "ok");
      }
    }
  });
});

describe("evidence freshness", () => {
  it("turns expired or aged evidence into unknown instead of keeping it fine", () => {
    const expired = readyEvidence();
    expired.extension.security!.expires_at = minutesAgo(1);
    const heartbeatGone = readyEvidence();
    heartbeatGone.connection!.last_heartbeat_at = minutesAgo(30);

    const security = assessGuard(expired, NOW).facts.find(
      (f) => f.key === "configuration",
    );
    expect(security?.state).toBe("unknown");

    // Every heartbeat-derived fact ages out together with the heartbeat.
    const aged = assessGuard(heartbeatGone, NOW);
    for (const key of [
      "bound",
      "connected",
      "operations",
      "updated",
    ] as const) {
      expect(aged.facts.find((f) => f.key === key)?.state).toBe("unknown");
    }
    expect(aged.ready).toBe(false);
  });

  it("tells a backup that aged past policy from one the backend called stale", () => {
    const evidence = readyEvidence();
    evidence.extension.recovery!.backup!.last_snapshot_at = hoursAgo(30);
    const fact = assessGuard(evidence, NOW).facts.find(
      (f) => f.key === "recoverable",
    );
    expect(fact?.state).toBe("attention");
  });

  it("reads Go, ISO and plain-second policy ages", () => {
    expect(parsePolicyAgeMs("24h")).toBe(86_400_000);
    expect(parsePolicyAgeMs("1h30m")).toBe(5_400_000);
    expect(parsePolicyAgeMs("PT12H")).toBe(43_200_000);
    expect(parsePolicyAgeMs(3600)).toBe(3_600_000);
    expect(parsePolicyAgeMs("whenever")).toBeUndefined();
  });
});

describe("legacy protection", () => {
  it("reports a bearer-only credential and a journal-less Guard as reduced, not fine", () => {
    const evidence = readyEvidence();
    evidence.worker.identity_binding = "legacy_bearer";
    evidence.worker.guard_operations = {
      durable: false,
      unresolved_outcome_unknown: 0,
      unacked_results: 0,
    };
    const { facts, ready } = assessGuard(evidence, NOW);
    for (const key of ["bound", "operations"] as const) {
      const fact = facts.find((f) => f.key === key);
      expect(fact?.state).toBe("attention");
      expect(fact?.reduced).toBe(true);
      expect(fact?.next).toBeDefined();
    }
    expect(ready).toBe(false);
  });

  it("does not let an unresolved outcome hide behind a healthy journal", () => {
    const evidence = readyEvidence();
    evidence.worker.guard_operations!.unresolved_outcome_unknown = 2;
    const fact = assessGuard(evidence, NOW).facts.find(
      (f) => f.key === "operations",
    );
    expect(fact?.state).toBe("attention");
  });
});

describe("canonical server read model", () => {
  it("reads a durable Guard as healthy from the read model alone", () => {
    // The shape internal/routes emits: the journal state is `state`.
    const server = {
      id: "srv-1",
      worker_id: "wrk-1",
      connection: { state: "connected", last_heartbeat_at: minutesAgo(1) },
      capabilities: {
        identity_binding: "key_bound",
        credential_generation: 1,
        guard_operations: {
          contract: "guard.operations/v1",
          state: "ok",
          durable: true,
          unresolved_outcome_unknown: 0,
          unacked_results: 0,
          in_flight: 0,
        },
      },
    } as unknown as CanonicalServer;
    const fact = assessGuard(collectGuardEvidence(server), NOW).facts.find(
      (f) => f.key === "operations",
    );
    expect(fact?.state).toBe("ok");
  });
});

describe("pairing ladder", () => {
  it("points at exactly the first unfinished step and blocks the rest", () => {
    const evidence = readyEvidence();
    delete evidence.extension.security;
    const ladder = pairingLadder(assessGuard(evidence, NOW));
    expect(ladder.steps.map((step) => step.state)).toEqual([
      "done",
      "done",
      "waiting",
      "done",
    ]);
    expect(ladder.complete).toBe(false);

    const broken = readyEvidence();
    broken.connection = {
      state: "degraded",
      reason_code: "relay_flapping",
      last_heartbeat_at: minutesAgo(1),
    };
    const failed = pairingLadder(assessGuard(broken, NOW));
    expect(failed.steps.find((s) => s.key === "connected")?.state).toBe(
      "failed",
    );
    expect(
      failed.steps.find((s) => s.key === "connected")?.fact.next,
    ).toBeDefined();
  });

  it("completes only when every rung is evidenced", () => {
    expect(pairingLadder(assessGuard(readyEvidence(), NOW)).complete).toBe(
      true,
    );
  });
});
