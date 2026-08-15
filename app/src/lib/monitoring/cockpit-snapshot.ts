import type { MonitoringCockpitPayload } from "$lib/api/monitoring";
import type { StackMetricValue, StackOperationServer } from "$lib/api/stacks";

const TERMINAL_OR_UNREACHABLE = new Set([
  "offline",
  "unreachable",
  "down",
  "decommissioning",
  "decommissioned",
  "absent",
]);

export interface CockpitSnapshotMerge {
  snapshot: MonitoringCockpitPayload;
  retainedTelemetry: boolean;
}

export interface CockpitRefreshError {
  message: string;
  status?: number;
  retryable: boolean;
}

export interface CockpitRefreshState {
  snapshot: MonitoringCockpitPayload | null;
  stackId: string;
  stale: boolean;
  error: CockpitRefreshError | null;
  generation: number;
}

export interface CockpitRefreshRequest {
  stackId: string;
  generation: number;
}

export interface CockpitRefreshStart {
  state: CockpitRefreshState;
  request: CockpitRefreshRequest;
}

interface RefreshErrorLike {
  message?: unknown;
  retryable?: unknown;
  status?: unknown;
}

function refreshErrorDetails(error: unknown): CockpitRefreshError {
  const candidate =
    typeof error === "object" && error !== null
      ? (error as RefreshErrorLike)
      : undefined;
  const status =
    typeof candidate?.status === "number" ? candidate.status : undefined;
  const message =
    typeof candidate?.message === "string" && candidate.message.trim()
      ? candidate.message
      : "Monitoring cockpit could not be refreshed.";
  const retryableStatuses = new Set([429, 502, 503, 504]);
  return {
    message,
    ...(status === undefined ? {} : { status }),
    retryable:
      candidate?.retryable === true ||
      (status === undefined ? true : retryableStatuses.has(status)),
  };
}

/**
 * Begin one stack-scoped cockpit refresh. A same-stack refresh retains the
 * last snapshot while the request is in flight; switching stacks is the only
 * start-time transition that clears it.
 */
export function beginCockpitRefresh(
  previous: CockpitRefreshState,
  stackId: string,
): CockpitRefreshStart {
  const generation = previous.generation + 1;
  const stackChanged = previous.stackId !== "" && previous.stackId !== stackId;
  return {
    request: { stackId, generation },
    state: {
      snapshot: stackChanged ? null : previous.snapshot,
      stackId,
      stale: stackChanged ? false : previous.stale,
      error: null,
      generation,
    },
  };
}

function isCurrentRefresh(
  state: CockpitRefreshState,
  request: CockpitRefreshRequest,
): boolean {
  return (
    state.generation === request.generation && state.stackId === request.stackId
  );
}

export function applyCockpitRefreshSuccess(
  state: CockpitRefreshState,
  request: CockpitRefreshRequest,
  next: MonitoringCockpitPayload,
  activeServerIDs?: ReadonlySet<string>,
): CockpitRefreshState {
  if (!isCurrentRefresh(state, request)) return state;
  const merged = mergeCockpitSnapshot(state.snapshot, next, activeServerIDs);
  return {
    ...state,
    snapshot: merged.snapshot,
    stale: merged.retainedTelemetry,
    error: null,
  };
}

export function applyCockpitRefreshError(
  state: CockpitRefreshState,
  request: CockpitRefreshRequest,
  error: unknown,
): CockpitRefreshState {
  if (!isCurrentRefresh(state, request)) return state;
  // A refresh error is not authoritative evidence that the stack or its
  // servers disappeared. Keep the same-stack snapshot and expose the error;
  // stack changes and successful terminal server evidence clear it elsewhere.
  return {
    ...state,
    stale: state.snapshot !== null,
    error: refreshErrorDetails(error),
  };
}

function metricKnown(metric?: StackMetricValue): boolean {
  return metric?.status === "ok" && typeof metric.value === "number";
}

function serverHasTerminalEvidence(server: StackOperationServer): boolean {
  return [server.health?.state, server.status]
    .map((state) => (state || "").trim().toLowerCase())
    .some((state) => TERMINAL_OR_UNREACHABLE.has(state));
}

function matchingServer(
  previous: StackOperationServer[],
  next: StackOperationServer,
): StackOperationServer | undefined {
  return previous.find((candidate) => {
    if (candidate.id && next.id) return candidate.id === next.id;
    return (
      (candidate.agent_id && candidate.agent_id === next.agent_id) ||
      (candidate.hostname && candidate.hostname === next.hostname)
    );
  });
}

function mergeMetric(
  next: StackMetricValue,
  previous: StackMetricValue,
): [StackMetricValue, boolean] {
  if (metricKnown(next) || !metricKnown(previous)) return [next, false];
  return [previous, true];
}

function mergeServerTelemetry(
  previous: StackOperationServer,
  next: StackOperationServer,
): [StackOperationServer, boolean] {
  if (serverHasTerminalEvidence(next)) {
    return [next, false];
  }

  const [cpu, retainedCPU] = mergeMetric(
    next.health.cpu_percent,
    previous.health.cpu_percent,
  );
  const [memory, retainedMemory] = mergeMetric(
    next.health.memory_percent,
    previous.health.memory_percent,
  );
  const [disk, retainedDisk] = mergeMetric(
    next.health.disk_percent,
    previous.health.disk_percent,
  );
  const [uptime, retainedUptime] = mergeMetric(
    next.health.uptime_seconds,
    previous.health.uptime_seconds,
  );
  const capabilities = { ...next.capabilities };
  let retainedCapabilities = false;
  for (const key of ["disk_gb", "ram_mb"] as const) {
    if (
      !(typeof capabilities[key] === "number" && capabilities[key] > 0) &&
      typeof previous.capabilities[key] === "number" &&
      previous.capabilities[key] > 0
    ) {
      capabilities[key] = previous.capabilities[key];
      retainedCapabilities = true;
    }
  }
  const retained =
    retainedCPU ||
    retainedMemory ||
    retainedDisk ||
    retainedUptime ||
    retainedCapabilities;
  if (!retained) return [next, false];
  return [
    {
      ...next,
      capabilities,
      health: {
        ...next.health,
        cpu_percent: cpu,
        memory_percent: memory,
        disk_percent: disk,
        uptime_seconds: uptime,
      },
    },
    true,
  ];
}

/**
 * Keep last verified capacity evidence across incomplete monitoring reads.
 * Explicit server unreachability/terminality always wins over retained data.
 */
export function mergeCockpitSnapshot(
  previous: MonitoringCockpitPayload | null,
  next: MonitoringCockpitPayload,
  activeServerIDs?: ReadonlySet<string>,
): CockpitSnapshotMerge {
  if (!previous || previous.techstack_id !== next.techstack_id) {
    return { snapshot: next, retainedTelemetry: false };
  }
  let retainedTelemetry = false;
  const matchedPrevious = new Set<StackOperationServer>();
  const servers = next.servers.map((server) => {
    const prior = matchingServer(previous.servers, server);
    if (!prior) return server;
    matchedPrevious.add(prior);
    const [merged, retained] = mergeServerTelemetry(prior, server);
    retainedTelemetry ||= retained;
    return merged;
  });
  for (const prior of previous.servers) {
    const stillCanonical = !activeServerIDs || activeServerIDs.has(prior.id);
    if (!matchedPrevious.has(prior) && stillCanonical) {
      servers.push(prior);
      retainedTelemetry = true;
    }
  }
  const hasTerminalEvidence = next.servers.some(serverHasTerminalEvidence);
  const snapshot = {
    ...next,
    servers,
    // A successful but partial cockpit response can carry zero/unknown
    // aggregates while server-level telemetry is still unavailable. Keep the
    // last verified telemetry projection until replacement values or explicit
    // down/absence evidence arrive. Canonical stack/job fields remain current.
    ...(retainedTelemetry && !hasTerminalEvidence
      ? {
          kpis: {
            ...next.kpis,
            healthy_servers: previous.kpis.healthy_servers,
            running_services: previous.kpis.running_services,
            active_alerts: previous.kpis.active_alerts,
          },
          monitoring: previous.monitoring,
          alerts: previous.alerts,
          readiness: previous.readiness,
        }
      : {}),
  };
  return {
    snapshot,
    retainedTelemetry,
  };
}
