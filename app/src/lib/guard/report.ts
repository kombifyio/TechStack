/**
 * kombify Guard evidence report.
 *
 * The report is assembled only from the persisted read model that the panel
 * itself shows: the seven facts, the findings, and the evidence sections they
 * were derived from, each with its observation time. It never includes a
 * credential, a token, an internal identifier beyond the server and worker ids,
 * or any field the panel does not display. Facts without fresh evidence stay
 * "not evidenced" in the report exactly as on screen.
 */

import type { GuardEvidence, GuardFinding } from "#lib/api/guard.js";
import {
  protectionNotices,
  type GuardAssessment,
  type GuardFact,
  type GuardOverall,
} from "./facts";
import { renderGuardMessage, type Translate } from "./render";

export interface GuardReportFact {
  key: GuardFact["key"];
  state: GuardFact["state"];
  reduced_protection: boolean;
  observed_at?: string;
  summary: string;
  details: string[];
  next_action?: string;
}

export interface GuardReport {
  kind: "kombify.guard.report";
  schema_version: 1;
  generated_at: string;
  locale: string;
  server: {
    id: string;
    name: string;
    worker_id?: string;
    lifecycle_state?: string;
  };
  guard_ready: boolean;
  overall: GuardOverall;
  facts: GuardReportFact[];
  protection_notices: string[];
  findings: GuardFinding[];
  evidence: {
    connection?: GuardEvidence["connection"];
    identity_binding?: string;
    operations?: {
      journal?: string;
      durable: boolean;
      unresolved_outcome_unknown: number;
      unacked_results: number;
    };
    posture?: {
      mode: string;
      update_state?: string;
      update_version?: string;
      deny_mutations: boolean;
    };
    security?: {
      state: string;
      reason_code?: string;
      baseline_version?: string;
      observed_at?: string;
      expires_at?: string;
    };
    recovery?: GuardEvidence["extension"]["recovery"];
    updates?: GuardEvidence["extension"]["updates"];
    reach?: {
      state: string;
      kombify_me_state?: string;
      kombify_me_address?: string;
      kombify_me_reason_code?: string;
      connect_state?: string;
      connect_reason_code?: string;
      relay_state?: string;
      relay_reason_code?: string;
      updated_at: string;
    };
  };
  /**
   * SHA-256 over the canonical form of everything above except
   * `generated_at` and `digest`, so two reports of the same persisted facts
   * carry the same value and an edited file can be told apart.
   */
  digest: {
    algorithm: "sha256";
    scope: "report-without-generated_at";
    value: string;
  };
}

export interface GuardReportInput {
  server: { id: string; name: string };
  evidence: GuardEvidence;
  assessment: GuardAssessment;
  generatedAt: Date;
  locale: string;
  translate: Translate;
}

/** JSON with object keys sorted, so equal content always serializes equally. */
export function canonicalJson(value: unknown): string {
  if (value === undefined) return "null";
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
  if (value && typeof value === "object") {
    const entries = Object.entries(value as Record<string, unknown>)
      .filter(([, entry]) => entry !== undefined)
      .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
    return `{${entries
      .map(([key, entry]) => `${JSON.stringify(key)}:${canonicalJson(entry)}`)
      .join(",")}}`;
  }
  return JSON.stringify(value);
}

export async function sha256Hex(text: string): Promise<string> {
  const bytes = new TextEncoder().encode(text);
  const digest = await crypto.subtle.digest("SHA-256", bytes);
  return Array.from(new Uint8Array(digest), (byte) =>
    byte.toString(16).padStart(2, "0"),
  ).join("");
}

function evidenceSection(evidence: GuardEvidence): GuardReport["evidence"] {
  const { worker, extension, reach, connection } = evidence;
  const out: GuardReport["evidence"] = {};
  if (connection) out.connection = { ...connection };
  if (worker.identity_binding) out.identity_binding = worker.identity_binding;
  if (worker.guard_operations) {
    const ops = worker.guard_operations;
    out.operations = {
      journal: ops.journal,
      durable: ops.durable,
      unresolved_outcome_unknown: ops.unresolved_outcome_unknown,
      unacked_results: ops.unacked_results,
    };
  }
  if (worker.guard_posture) {
    const posture = worker.guard_posture;
    out.posture = {
      mode: posture.mode,
      update_state: posture.update_state,
      update_version: posture.update_version,
      deny_mutations: posture.deny_mutations,
    };
  }
  if (extension.security) {
    const { findings: _findings, ...security } = extension.security;
    out.security = security;
  }
  if (extension.recovery) out.recovery = extension.recovery;
  if (extension.updates) out.updates = extension.updates;
  if (reach) {
    out.reach = {
      state: reach.state,
      kombify_me_state: reach.kombify_me?.state,
      kombify_me_address: reach.kombify_me?.address,
      kombify_me_reason_code: reach.kombify_me?.reason_code,
      connect_state: reach.connect?.state,
      connect_reason_code: reach.connect?.reason_code,
      relay_state: reach.relay?.state,
      relay_reason_code: reach.relay?.reason_code,
      updated_at: reach.updated_at,
    };
  }
  return out;
}

export async function buildGuardReport(
  input: GuardReportInput,
): Promise<GuardReport> {
  const { evidence, assessment, translate } = input;
  const isoTime = (iso: string) => iso;
  const say = (message: Parameters<typeof renderGuardMessage>[0]) =>
    renderGuardMessage(message, translate, isoTime);
  const body = {
    kind: "kombify.guard.report" as const,
    schema_version: 1 as const,
    locale: input.locale,
    server: {
      id: input.server.id,
      name: input.server.name,
      worker_id: evidence.worker_id,
      lifecycle_state: evidence.lifecycle_state,
    },
    guard_ready: assessment.ready,
    overall: assessment.overall,
    facts: assessment.facts.map((fact): GuardReportFact => ({
      key: fact.key,
      state: fact.state,
      reduced_protection: fact.reduced,
      observed_at: fact.observedAt,
      summary: say(fact.reason),
      details: fact.details.map(say),
      next_action: fact.next ? say(fact.next) : undefined,
    })),
    protection_notices: protectionNotices(evidence).map(say),
    findings: evidence.extension.security?.findings ?? [],
    evidence: evidenceSection(evidence),
  };
  const value = await sha256Hex(canonicalJson(body));
  return {
    ...body,
    generated_at: input.generatedAt.toISOString(),
    digest: {
      algorithm: "sha256",
      scope: "report-without-generated_at",
      value,
    },
  };
}

export function reportFileName(report: GuardReport): string {
  const safe = report.server.name
    .replace(/[^A-Za-z0-9._-]+/g, "-")
    .slice(0, 48);
  return `kombify-guard-report-${safe || report.server.id}.json`;
}

// ---------------------------------------------------------------------------
// Printable HTML view
// ---------------------------------------------------------------------------

const ESCAPES: Record<string, string> = {
  "&": "&amp;",
  "<": "&lt;",
  ">": "&gt;",
  '"': "&quot;",
  "'": "&#39;",
};

export function escapeHtml(value: unknown): string {
  return String(value ?? "").replace(/[&<>"']/g, (char) => ESCAPES[char]!);
}

const GLYPH: Record<GuardFact["state"], string> = {
  ok: "✓",
  attention: "▲",
  unknown: "○",
};

/**
 * A self-contained HTML document for printing or saving. Every value is
 * escaped; state is conveyed by a glyph and a word, never by color alone.
 */
export function renderGuardReportHtml(
  report: GuardReport,
  translate: Translate,
  dir: "ltr" | "rtl",
): string {
  const t = (id: string, params?: Record<string, string | number>) =>
    escapeHtml(translate(id, params));
  const factRows = report.facts
    .map((fact) => {
      const details = fact.details
        .map((line) => `<li>${escapeHtml(line)}</li>`)
        .join("");
      return `<tr>
  <th scope="row">${t(`guard.fact.${fact.key}.title`)}</th>
  <td><span class="state state-${fact.state}"><span aria-hidden="true">${GLYPH[fact.state]}</span> ${t(`guard.state.${fact.state}`)}</span>${
    fact.reduced_protection
      ? ` <span class="reduced">${t("guard.reduced")}</span>`
      : ""
  }</td>
  <td>${escapeHtml(fact.summary)}${details ? `<ul>${details}</ul>` : ""}${
    fact.next_action
      ? `<p class="next">${escapeHtml(fact.next_action)}</p>`
      : ""
  }</td>
  <td class="mono">${fact.observed_at ? escapeHtml(fact.observed_at) : t("guard.report.noObservation")}</td>
</tr>`;
    })
    .join("\n");

  const findingRows = report.findings
    .map(
      (finding) => `<tr>
  <th scope="row" class="mono">${escapeHtml(finding.control)}</th>
  <td>${escapeHtml(finding.state)} / ${escapeHtml(finding.severity)}</td>
  <td>${escapeHtml(finding.expected ?? "")}</td>
  <td>${escapeHtml(finding.observed ?? "")}</td>
  <td>${escapeHtml(finding.remediation ?? "")}</td>
</tr>`,
    )
    .join("\n");

  const notices = report.protection_notices
    .map((line) => `<li>${escapeHtml(line)}</li>`)
    .join("");

  return `<!doctype html>
<html lang="${escapeHtml(report.locale)}" dir="${dir}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${t("guard.report.title")} - ${escapeHtml(report.server.name)}</title>
<style>
  :root { color-scheme: light; }
  body { font: 14px/1.5 system-ui, sans-serif; color: #111; background: #fff; margin: 0; padding: 24px; }
  main { max-width: 960px; margin: 0 auto; }
  h1 { font-size: 22px; margin: 0 0 4px; }
  h2 { font-size: 16px; margin: 28px 0 8px; }
  table { width: 100%; border-collapse: collapse; }
  th, td { text-align: start; vertical-align: top; padding: 8px; border-top: 1px solid #ccc; }
  thead th { border-top: 0; border-bottom: 2px solid #111; }
  ul { margin: 4px 0 0; padding-inline-start: 18px; }
  .mono { font-family: ui-monospace, monospace; font-size: 12px; word-break: break-all; }
  .state { font-weight: 600; white-space: nowrap; }
  .state-attention { text-decoration: underline; }
  .reduced { border: 1px solid #111; padding: 0 6px; font-size: 12px; }
  .next { margin: 6px 0 0; font-style: italic; }
  .meta { color: #444; margin: 0 0 16px; }
  .note { margin-top: 24px; font-size: 12px; color: #444; }
  @media print { body { padding: 0; } }
</style>
</head>
<body>
<main>
<h1>${t("guard.report.title")}</h1>
<p class="meta">${escapeHtml(report.server.name)} (<span class="mono">${escapeHtml(report.server.id)}</span>)<br>
${t("guard.report.generated", { at: report.generated_at })}</p>
<p><strong>${t(`guard.overall.${report.overall}`)}</strong></p>
<h2>${t("guard.report.facts")}</h2>
<table>
<thead><tr><th>${t("guard.report.colFact")}</th><th>${t("guard.report.colState")}</th><th>${t("guard.report.colEvidence")}</th><th>${t("guard.report.colObserved")}</th></tr></thead>
<tbody>
${factRows}
</tbody>
</table>
${notices ? `<h2>${t("guard.report.notices")}</h2><ul>${notices}</ul>` : ""}
<h2>${t("guard.report.findings")}</h2>
${
  findingRows
    ? `<table><thead><tr><th>${t("guard.findings.control")}</th><th>${t("guard.report.colState")}</th><th>${t("guard.findings.expected")}</th><th>${t("guard.findings.observed")}</th><th>${t("guard.findings.remediation")}</th></tr></thead><tbody>${findingRows}</tbody></table>`
    : `<p>${t("guard.report.noFindings")}</p>`
}
<p class="note">${t("guard.report.note")}</p>
<p class="note mono">${t("guard.report.digest")}: sha256:${escapeHtml(report.digest.value)}</p>
</main>
</body>
</html>
`;
}
