/**
 * kombify-TechStack API - Drift Detection Module
 *
 * API functions for infrastructure drift detection and resolution.
 * Part of Sprint 6: Terramate Integration.
 */

import { fetchApi } from "./client";
import type { ApiResponse } from "./client";

// ============================================================================
// Drift Detection Types
// ============================================================================

/**
 * Drift status enum matching backend DriftStatus
 */
export type DriftStatus =
  | "in_sync"
  | "drifted"
  | "failed"
  | "checking"
  | "unknown";

/**
 * Trigger type for drift checks
 */
export type TriggerType = "manual" | "scheduled" | "webhook";

/**
 * Resource change detected during drift check
 */
export interface ResourceChange {
  address: string;
  resource_type: string;
  change_type: "create" | "update" | "delete" | "no-op";
  before?: Record<string, unknown>;
  after?: Record<string, unknown>;
}

/**
 * Summary of plan output
 */
export interface PlanSummary {
  to_create: number;
  to_update: number;
  to_delete: number;
  unchanged: number;
}

/**
 * Drift check result from the API
 */
export interface DriftResult {
  id: string;
  stack_id: string;
  stack_name?: string;
  status: DriftStatus;
  affected_count: number;
  affected_resources?: ResourceChange[];
  plan_summary?: PlanSummary;
  plan_output?: Record<string, unknown>;
  error_message?: string;
  error_details?: string;
  trigger_type: TriggerType;
  checked_at: string;
  duration_ms: number;
}

/**
 * Drift status response from stack-specific endpoint
 */
export interface DriftStatusResponse {
  stack_id: string;
  stack_name: string;
  drift_status: DriftStatus;
  last_checked?: string;
  affected_count?: number;
  has_drift_result: boolean;
  drift_result_id?: string;
}

/**
 * Paginated list response for drift results
 */
export interface DriftResultListResponse {
  items: DriftResult[];
  total_items: number;
  total_pages: number;
  page: number;
  per_page: number;
}

/**
 * Response when triggering a drift check
 */
export interface TriggerDriftCheckResponse {
  job_id: string;
  stack_id: string;
  message: string;
  status: string;
}

// ============================================================================
// Drift API Functions
// ============================================================================

/**
 * Trigger a drift check for a specific stack
 */
export async function triggerDriftCheck(
  stackId: string,
): Promise<TriggerDriftCheckResponse> {
  const res = await fetchApi<TriggerDriftCheckResponse>(
    `/api/v1/stacks/${stackId}/drift/check`,
    {
      method: "POST",
    },
  );
  return res.data;
}

/**
 * Get drift status for a specific stack
 */
export async function getDriftStatus(
  stackId: string,
): Promise<DriftStatusResponse> {
  const res = await fetchApi<DriftStatusResponse>(
    `/api/v1/stacks/${stackId}/drift/status`,
  );
  return res.data;
}

/**
 * Get detailed drift information for a stack
 */
export async function getDriftDetails(stackId: string): Promise<DriftResult> {
  const res = await fetchApi<DriftResult>(
    `/api/v1/stacks/${stackId}/drift/details`,
  );
  return res.data;
}

/**
 * Trigger drift resolution (apply changes) for a stack
 */
export async function triggerDriftResolve(
  stackId: string,
): Promise<TriggerDriftCheckResponse> {
  const res = await fetchApi<TriggerDriftCheckResponse>(
    `/api/v1/stacks/${stackId}/drift/resolve`,
    {
      method: "POST",
    },
  );
  return res.data;
}

/**
 * List all drift results with pagination
 */
export async function listDriftResults(
  page = 1,
  perPage = 20,
): Promise<DriftResultListResponse> {
  const res = await fetchApi<DriftResultListResponse>(
    `/api/v1/drift/results?page=${page}&per_page=${perPage}`,
  );
  return res.data;
}

/**
 * Get a specific drift result by ID
 */
export async function getDriftResult(resultId: string): Promise<DriftResult> {
  const res = await fetchApi<DriftResult>(`/api/v1/drift/results/${resultId}`);
  return res.data;
}

/**
 * Delete a drift result
 */
export async function deleteDriftResult(resultId: string): Promise<void> {
  await fetchApi(`/api/v1/drift/results/${resultId}`, {
    method: "DELETE",
  });
}

// ============================================================================
// Utility Functions
// ============================================================================

/**
 * Get human-readable status label
 */
export function getDriftStatusLabel(status: DriftStatus): string {
  const labels: Record<DriftStatus, string> = {
    in_sync: "In Sync",
    drifted: "Drifted",
    failed: "Failed",
    checking: "Checking...",
    unknown: "Unknown",
  };
  return labels[status] ?? status;
}

/**
 * Get status color for UI rendering
 */
export function getDriftStatusColor(
  status: DriftStatus,
): "green" | "yellow" | "red" | "gray" | "blue" {
  const colors: Record<
    DriftStatus,
    "green" | "yellow" | "red" | "gray" | "blue"
  > = {
    in_sync: "green",
    drifted: "yellow",
    failed: "red",
    checking: "blue",
    unknown: "gray",
  };
  return colors[status] ?? "gray";
}

/**
 * Format duration from milliseconds to human-readable
 */
export function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`;
  const minutes = Math.floor(ms / 60000);
  const seconds = Math.round((ms % 60000) / 1000);
  return `${minutes}m ${seconds}s`;
}
