/**
 * kombify-TechStack API - Monitoring Module
 *
 * Client for the Monitoring 2.0 PromQL query API, metric enumeration,
 * TSDB status, and alert endpoints.
 */

import { fetchApi } from "./client";
import {
  normalizeStackOperationServer,
  type KitDeployment,
  type StackNextStep,
  type StackOperationAlert,
  type StackOperationKPIs,
  type StackOperationMonitoring,
  type StackOperationServer,
  type StackOperationServerWire,
  type StackOperationService,
  type StackReadiness,
} from "./stacks";

// ============================================================================
// Types
// ============================================================================

export interface TSDBStatus {
  seriesCount: number;
  minTimeMs: number;
  maxTimeMs: number;
  retentionDays: number;
  topMetrics: { Name: string; Count: number }[];
  queryBackend: string;
  queryBackendURL?: string;
  ingestBackend: string;
  collectorMode: "direct" | "gateway" | "mixed";
  compatibilityMode: string;
}

export interface IngestLaneHealth {
  status: "ok" | "idle" | "degraded" | "error" | "unavailable" | string;
  queueMode: string;
  queueDepth: number;
  queueCapacity: number;
  requestsAccepted: number;
  samplesAccepted: number;
  requestsRejected: number;
  samplesRejected: number;
  parseErrors: number;
  lastSuccessAt?: string;
  lastFailureAt?: string;
  lastError?: string;
}

export interface MonitorHealth {
  queryBackend: string;
  queryBackendStatus: "ok" | "error" | string;
  queryBackendError?: string;
  ingestBackend: string;
  collectorMode: "direct" | "gateway" | "mixed";
  compatibilityMode: string;
  ingestStatus: "ok" | "idle" | "degraded" | "unavailable" | string;
  otlp?: IngestLaneHealth;
  legacyPush?: IngestLaneHealth;
}

export interface PromQLPoint {
  /** Unix milliseconds. */
  at: number;
  value: number;
}

export interface PromQLSeries {
  metric: Record<string, string>;
  points: PromQLPoint[];
}

export interface PromQLResult {
  resultType: string;
  /**
   * Prometheus text form. Kept for compatibility only — it is an internal
   * representation, not a contract. Anything that draws reads `series`.
   */
  result: string;
  series?: PromQLSeries[];
}

/** The windows the availability projection offers. It is a closed set. */
export type AvailabilityWindow = "24h" | "7d" | "30d" | "90d";

export interface AvailabilityEpisode {
  server_id: string;
  server_name?: string;
  started_at: string;
  /** Absent while ongoing; never backfilled from the window edge. */
  ended_at?: string;
  seconds: number;
  ongoing: boolean;
  /** Worst connection state the episode reached. */
  state: string;
  trigger_reason?: string;
  trigger_source?: string;
  recovery_reason?: string;
  recovery_source?: string;
  evidence_ref?: string;
}

export interface AvailabilityDay {
  day: string;
  /** Worst state observed that UTC day; empty means no observation. */
  state: string;
}

export interface AvailabilityServer {
  server_id: string;
  name?: string;
  /** Null when the window carries no observed time — not the same as zero. */
  uptime_ratio: number | null;
  observed_seconds: number;
  down_seconds: number;
  episodes: AvailabilityEpisode[];
  days: AvailabilityDay[];
}

export interface AvailabilityCause {
  reason_code: string;
  seconds: number;
  episodes: number;
}

export interface AvailabilityReport {
  window: AvailabilityWindow;
  start: string;
  end: string;
  uptime_ratio: number | null;
  observed_seconds: number;
  down_seconds: number;
  /** Mean recovery time over closed episodes only. */
  mttr_seconds: number | null;
  closed_episodes: number;
  by_cause: AvailabilityCause[];
  servers: AvailabilityServer[];
  /** How each connection state was counted, published with the numbers. */
  accounting: { up: string[]; down: string[]; unobserved: string[] };
}

export interface AlertState {
  rule: AlertRule;
  active: boolean;
  value: number;
  active_since: string | null;
  fired_at: string | null;
  last_eval: string;
}

export interface AlertRule {
  name: string;
  expr: string;
  for: number; // nanoseconds
  severity: string;
  labels: Record<string, string> | null;
  message: string;
}

export interface MonitoringCockpitJob {
  id: string;
  type: string;
  state: string;
  progress: number;
  step?: string;
  message?: string;
  error?: string;
  wait_reason?: string;
  next_resume_at?: string;
  resume_available_at?: string;
  resume_available?: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface MonitoringCockpitPayload {
  homelab_id?: string;
  kit_deployment_id?: string;
  kit_deployment_count: number;
  connected_server_count: number;
  /** Deprecated deployment-scoped compatibility fields. */
  stacks?: Array<KitDeployment & { status?: string; state?: string }>;
  stack?: KitDeployment & { status?: string; state?: string };
  readiness?: StackReadiness;
  nextSteps: StackNextStep[];
  kpis: StackOperationKPIs;
  servers: StackOperationServer[];
  services: StackOperationService[];
  monitoring: StackOperationMonitoring;
  alerts: StackOperationAlert[];
  jobs: MonitoringCockpitJob[];
}

type MonitoringCockpitPayloadWire = Omit<
  MonitoringCockpitPayload,
  "stacks" | "stack" | "servers"
> & {
  stacks?: KitDeployment[];
  stack?: KitDeployment;
  servers: StackOperationServerWire[];
};

// ============================================================================
// API Functions
// ============================================================================

/** Get TSDB status and diagnostics */
export async function getMonitorStatus(): Promise<TSDBStatus> {
  const res = await fetchApi<TSDBStatus>("/api/v1/monitor/status");
  return res.data;
}

/** Get monitoring health across query and ingest lanes */
export async function getMonitorHealth(): Promise<MonitorHealth> {
  const res = await fetchApi<MonitorHealth>("/api/v1/monitor/health");
  return res.data;
}

/**
 * Get the owner-scoped Homelab monitoring cockpit. Passing a deployment ID is
 * retained only for older deployment-scoped consumers during migration.
 */
export async function getMonitoringCockpit(
  kitDeploymentId?: string,
): Promise<MonitoringCockpitPayload> {
  const params = new URLSearchParams();
  if (kitDeploymentId) params.set("kit_deployment_id", kitDeploymentId);
  const suffix = params.toString() ? `?${params.toString()}` : "";
  const res = await fetchApi<MonitoringCockpitPayloadWire>(
    `/api/v1/monitor/cockpit${suffix}`,
  );
  const { servers, ...payload } = res.data;
  return {
    ...payload,
    servers: servers.map(normalizeStackOperationServer),
  };
}

/** Execute an instant PromQL query */
export async function instantQuery(
  query: string,
  time?: string,
): Promise<PromQLResult> {
  const params = new URLSearchParams({ q: query });
  if (time) params.set("time", time);
  const res = await fetchApi<PromQLResult>(
    `/api/v1/monitor/metrics/instant?${params}`,
  );
  return res.data;
}

/** Execute a range PromQL query */
export async function rangeQuery(
  query: string,
  start?: string,
  end?: string,
  step?: string,
): Promise<PromQLResult> {
  const params = new URLSearchParams({ q: query });
  if (start) params.set("start", start);
  if (end) params.set("end", end);
  if (step) params.set("step", step);
  const res = await fetchApi<PromQLResult>(
    `/api/v1/monitor/metrics/query?${params}`,
  );
  return res.data;
}

/** Get all metric names in the TSDB */
export async function getMetricNames(): Promise<string[]> {
  const res = await fetchApi<string[]>("/api/v1/monitor/metrics/names");
  return res.data ?? [];
}

/** Get all label names */
export async function getLabelNames(): Promise<string[]> {
  const res = await fetchApi<string[]>("/api/v1/monitor/metrics/labels");
  return res.data ?? [];
}

/** Get values for a specific label */
export async function getLabelValues(name: string): Promise<string[]> {
  const res = await fetchApi<string[]>(
    `/api/v1/monitor/metrics/values?name=${encodeURIComponent(name)}`,
  );
  return res.data ?? [];
}

/** Get active (firing) alerts */
export async function getActiveAlerts(): Promise<AlertState[]> {
  const res = await fetchApi<AlertState[]>("/api/v1/monitor/alerts");
  return res.data ?? [];
}

/** Get all alert rule states */
export async function getAlertRules(): Promise<AlertState[]> {
  const res = await fetchApi<AlertState[]>("/api/v1/monitor/alerts/rules");
  return res.data ?? [];
}

/**
 * Availability, downtime episodes and daily state over a window.
 *
 * The report is a projection over the recorded transition timeline, so an
 * episode is evidence rather than a sample. Pass `serverId` for the per-server
 * detail; omit it for the fleet.
 */
export async function getAvailabilityReport(
  window: AvailabilityWindow = "30d",
  serverId?: string,
): Promise<AvailabilityReport> {
  const params = new URLSearchParams({ window });
  if (serverId) params.set("server_id", serverId);
  const res = await fetchApi<AvailabilityReport>(
    `/api/v1/monitor/availability?${params}`,
  );
  return res.data;
}
