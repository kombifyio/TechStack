/**
 * kombify-TechStack API - Monitoring Module
 *
 * Client for the Monitoring 2.0 PromQL query API, metric enumeration,
 * TSDB status, and alert endpoints.
 */

import { fetchApi } from "./client";
import type {
  Stack,
  StackNextStep,
  StackOperationAlert,
  StackOperationKPIs,
  StackOperationMonitoring,
  StackOperationServer,
  StackOperationService,
  StackReadiness,
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

export interface PromQLResult {
  resultType: string;
  result: string;
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
  stacks: Array<Stack & { status?: string; state?: string }>;
  techstack_id: string;
  stack?: Stack & { status?: string; state?: string };
  readiness?: StackReadiness;
  nextSteps: StackNextStep[];
  kpis: StackOperationKPIs;
  servers: StackOperationServer[];
  services: StackOperationService[];
  monitoring: StackOperationMonitoring;
  alerts: StackOperationAlert[];
  jobs: MonitoringCockpitJob[];
}

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

/** Get the stack-scoped Homelab monitoring cockpit. */
export async function getMonitoringCockpit(
  techstackId?: string,
): Promise<MonitoringCockpitPayload> {
  const params = new URLSearchParams();
  if (techstackId) params.set("techstack_id", techstackId);
  const suffix = params.toString() ? `?${params.toString()}` : "";
  const res = await fetchApi<MonitoringCockpitPayload>(
    `/api/v1/monitor/cockpit${suffix}`,
  );
  return res.data;
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
