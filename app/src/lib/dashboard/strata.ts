/**
 * Data for the Strata dashboard: the per-Node 24-hour CPU curve and the
 * last-24-hours event line. Everything drawn comes from a recorded series or
 * event; a missing series is drawn flat and labelled, never synthesised.
 */
import {
  getActiveAlerts,
  getAvailabilityReport,
  getMonitoringCockpit,
  rangeQuery,
  type PromQLPoint,
} from "#lib/api/monitoring.js";
import { statusLabel } from "#lib/server-card-adapter.js";

import { tr, trn } from "#lib/i18n.svelte.js";
export const DAY_MS = 24 * 60 * 60 * 1000;

/** The per-Node CPU query the Node monitoring page plots. */
export function nodeCpuQuery(nodeId: string): string {
  const escaped = nodeId.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
  return `node_cpu_usage_percent{node_id="${escaped}",core="total"}`;
}

export type CpuSeries =
  | { state: "ready"; points: PromQLPoint[] }
  | { state: "empty" }
  | { state: "error" };

export async function loadCpuSeries(
  nodeId: string,
  end = new Date(),
): Promise<CpuSeries> {
  const start = new Date(end.getTime() - DAY_MS);
  try {
    const result = await rangeQuery(
      nodeCpuQuery(nodeId),
      start.toISOString(),
      end.toISOString(),
      "10m",
    );
    // Several series (per mount, per core) are possible; draw the busiest
    // one, as the Node monitoring page does, never an average of them.
    let best: PromQLPoint[] = [];
    let bestPeak = -Infinity;
    for (const series of result.series ?? []) {
      const points = series.points.filter((point) =>
        Number.isFinite(point.value),
      );
      const peak = Math.max(-Infinity, ...points.map((point) => point.value));
      if (points.length > 1 && peak > bestPeak) {
        best = points;
        bestPeak = peak;
      }
    }
    return best.length > 1
      ? { state: "ready", points: best }
      : { state: "empty" };
  } catch {
    return { state: "error" };
  }
}

/** SVG path for a CPU curve in a 800x70 box over [start, end]. */
export function cpuCurvePath(
  points: readonly PromQLPoint[],
  start: number,
  end: number,
): { line: string; area: string } {
  const span = Math.max(1, end - start);
  const coords = points
    .filter((point) => point.at >= start && point.at <= end)
    .map((point) => {
      const x = ((point.at - start) / span) * 800;
      const clamped = Math.max(0, Math.min(100, point.value));
      const y = 66 - (clamped / 100) * 62;
      return `${x.toFixed(1)} ${y.toFixed(1)}`;
    });
  if (coords.length < 2) return { line: "", area: "" };
  const line = `M${coords.join(" L")}`;
  const firstX = coords[0]!.split(" ")[0];
  const lastX = coords.at(-1)!.split(" ")[0];
  return { line, area: `${line} L${lastX} 70 L${firstX} 70 Z` };
}

export type TimelineTone = "ok" | "info" | "warn" | "error";

export interface TimelineEvent {
  id: string;
  at: number;
  label: string;
  tone: TimelineTone;
}

export interface Timeline {
  events: TimelineEvent[];
  /** Sources that could not be read; the line says so instead of looking calm. */
  unavailable: string[];
}

const JOB_LABELS: Record<string, string> = {
  get deploy() {
    return tr("ui.strata.rollout");
  },
  get provision() {
    return tr("ui.strata.provisioning");
  },
  get restore() {
    return tr("ui.strata.restore");
  },
  get backup() {
    return tr("ui.strata.backup");
  },
  get upgrade() {
    return tr("ui.strata.upgrade");
  },
  get destroy() {
    return tr("ui.strata.removal");
  },
  get decommission() {
    return tr("ui.stacksId.decommission");
  },
};

function jobTone(state: string): TimelineTone {
  const value = state.toLowerCase();
  if (["failed", "error", "cancelled"].includes(value)) return "error";
  if (["running", "pending", "queued"].includes(value)) return "warn";
  if (["completed", "succeeded", "done"].includes(value)) return "ok";
  return "info";
}

function inWindow(at: number, start: number, end: number): boolean {
  return Number.isFinite(at) && at >= start && at <= end;
}

export async function loadTimeline(now = Date.now()): Promise<Timeline> {
  const start = now - DAY_MS;
  const [cockpit, alerts, availability] = await Promise.allSettled([
    getMonitoringCockpit(),
    getActiveAlerts(),
    getAvailabilityReport("24h"),
  ]);
  const events: TimelineEvent[] = [];
  const unavailable: string[] = [];

  if (cockpit.status === "fulfilled") {
    for (const job of cockpit.value.jobs ?? []) {
      const at = Date.parse(job.updated_at || job.created_at || "");
      if (!inWindow(at, start, now)) continue;
      const kind =
        JOB_LABELS[job.type] ?? statusLabel(job.type || tr("ui.strata.job"));
      events.push({
        id: `job:${job.id}`,
        at,
        label: `${kind} ${statusLabel(job.state || "")}`.trim(),
        tone: jobTone(job.state || ""),
      });
    }
  } else {
    unavailable.push("operations");
  }

  if (alerts.status === "fulfilled") {
    for (const alert of alerts.value) {
      const at = Date.parse(alert.fired_at || alert.active_since || "");
      if (!inWindow(at, start, now)) continue;
      events.push({
        id: `alert:${alert.rule.name}:${at}`,
        at,
        label: alert.rule.message || statusLabel(alert.rule.name),
        tone: alert.rule.severity === "critical" ? "error" : "warn",
      });
    }
  } else {
    unavailable.push("alerts");
  }

  if (availability.status === "fulfilled") {
    for (const server of availability.value.servers ?? []) {
      for (const episode of server.episodes ?? []) {
        const at = Date.parse(episode.started_at);
        if (!inWindow(at, start, now)) continue;
        const name = episode.server_name || server.name || episode.server_id;
        events.push({
          id: `episode:${episode.server_id}:${episode.started_at}`,
          at,
          label: `${name} ${statusLabel(episode.state || "offline")}`,
          tone: episode.ongoing ? "error" : "warn",
        });
      }
    }
  } else {
    unavailable.push("availability");
  }

  events.sort((left, right) => left.at - right.at);
  return { events, unavailable };
}

export interface StatusFacts {
  nodes: number;
  unhealthyNodes: number;
  alerts: number;
  rolloutNode: string | null;
  failureNode: string | null;
}

/**
 * One sentence built from real state, most urgent fact first. Empty when
 * there is nothing to say: "all healthy" is what the Nodes stat and its
 * lamps already show (owner direction 2026-09-26).
 */
export function statusSentence(facts: StatusFacts): string {
  if (facts.nodes === 0) return tr("ui.strata.noNodeHasReportedYet");
  const parts: string[] = [];
  if (facts.failureNode) {
    parts.push(
      tr("ui.strata.theLastRolloutOnFailed", {
        failureNode: facts.failureNode,
      }),
    );
  }
  if (facts.alerts > 0) {
    parts.push(trn("ui.strata.alertsActive", facts.alerts));
  }
  if (facts.unhealthyNodes > 0) {
    parts.push(
      trn("ui.strata.nodesNeedAttention", facts.unhealthyNodes, {
        total: facts.nodes,
      }),
    );
  }
  if (facts.rolloutNode) {
    parts.push(
      tr("ui.strata.aRolloutIsInProgress", { rolloutNode: facts.rolloutNode }),
    );
  }
  return parts.join(" ");
}
