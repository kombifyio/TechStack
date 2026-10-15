import { describe, expect, it } from "vitest";

import { collectGuardEvidence, type GuardEvidence } from "#lib/api/guard.js";
import type { CanonicalServer } from "#lib/api/registry.js";
import { assessGuard } from "./facts";
import { buildGuardReport } from "./report";

const NOW = new Date("2026-09-30T12:00:00Z");
const translate = (id: string) => id;

function evidenceFor(): GuardEvidence {
  const server = {
    id: "srv-1",
    worker_id: "wrk-1",
    lifecycle: { state: "active", desired_state: "active" },
    connection: {
      state: "connected",
      changed_at: NOW.toISOString(),
      last_heartbeat_at: NOW.toISOString(),
    },
    // Fields a report must never carry over, whatever the read model holds.
    agent_token: "kgt_secret_token",
    security: {
      state: "drifted",
      observed_at: NOW.toISOString(),
      baseline_version: "2026.09",
      private_note: "internal-only",
      findings: [
        {
          control: "sshd.password_auth",
          state: "drifted",
          severity: "high",
          expected: "no",
          observed: "yes",
          remediation: "Disable password login",
          exception: {
            reason: "lab",
            approved_by: "owner",
            session_token: "abc",
          },
        },
      ],
    },
  } as unknown as CanonicalServer;
  return collectGuardEvidence(server, {
    identity_binding: "host_key",
    credential_generation: 7,
    agent_token: "kgt_secret_token",
  });
}

describe("Guard evidence report", () => {
  it("carries only the facts the panel shows, and a digest that follows them", async () => {
    const evidence = evidenceFor();
    const assessment = assessGuard(evidence, NOW.getTime());
    const make = (generatedAt: Date) =>
      buildGuardReport({
        server: { id: "srv-1", name: "nas" },
        evidence,
        assessment,
        generatedAt,
        locale: "en",
        translate,
      });

    const first = await make(NOW);
    const later = await make(new Date(NOW.getTime() + 86_400_000));
    const body = JSON.stringify(first);

    // Nothing outside the displayed fields survives, secrets least of all.
    for (const leaked of [
      "kgt_secret_token",
      "internal-only",
      "session_token",
      "credential_generation",
    ]) {
      expect(body).not.toContain(leaked);
    }
    // The same persisted facts hash the same, whenever the report is made.
    expect(later.digest.value).toBe(first.digest.value);

    // A changed fact changes the digest.
    const drifted = { ...evidence, extension: { ...evidence.extension } };
    drifted.extension.security = {
      ...evidence.extension.security!,
      state: "compliant",
      findings: [],
    };
    const other = await buildGuardReport({
      server: { id: "srv-1", name: "nas" },
      evidence: drifted,
      assessment: assessGuard(drifted, NOW.getTime()),
      generatedAt: NOW,
      locale: "en",
      translate,
    });
    expect(other.digest.value).not.toBe(first.digest.value);
  });
});
