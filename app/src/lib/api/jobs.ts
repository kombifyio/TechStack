/**
 * kombify-TechStack API - Jobs Module
 *
 * Job management and tracking through the TechStack BFF.
 */

import { fetchApi, API_BASE } from "./client";

// ============================================================================
// Types
// ============================================================================

export interface Job {
  id: string;
  type: string;
  state: string;
  progress: number;
  stack_id?: string;
  target_id?: string;
  error?: string;
  wait_reason?: string;
  next_resume_at?: string;
  resume_available_at?: string;
  resume_available?: boolean;
  created_at: string;
  updated_at: string;
}

export interface JobDetail extends Job {
  payload?: Record<string, unknown>;
  result?: Record<string, unknown> | string;
  step?: string; // Current step ID for progress tracking
  current_step?: string; // Legacy PocketBase job field returned by v1 job APIs
  message?: string; // Status message
  error_details?: string; // Detailed error information for troubleshooting
  logs?: string[];
}

interface JobDetailApiResponse {
  id: string;
  type?: string;
  state?: string;
  progress?: number;
  error?: string;
  stack_id?: string;
  target_id?: string;
  payload?: Record<string, unknown>;
  result?: Record<string, unknown> | string;
  step?: string;
  current_step?: string;
  message?: string;
  error_details?: string;
  logs?: string[];
  wait_reason?: string;
  next_resume_at?: string;
  resume_available_at?: string;
  resume_available?: boolean;
  created?: string;
  updated?: string;
  created_at?: string;
  updated_at?: string;
}

export interface JobsListRequest {
  page?: number;
  perPage?: number;
  limit?: number;
  stackId?: string;
  state?: string;
  type?: string;
  search?: string;
}

export interface JobsListResult {
  items: JobDetail[];
  page: number;
  perPage: number;
  totalItems: number;
  totalPages: number;
}

interface JobsListApiResponse {
  items?: JobDetailApiResponse[];
  page?: number;
  per_page?: number;
  total_items?: number;
  total_pages?: number;
}

function normalizeJobDetail(job: JobDetailApiResponse): JobDetail {
  return {
    id: job.id,
    type: job.type ?? "",
    state: job.state ?? "pending",
    progress: job.progress ?? 0,
    stack_id: job.stack_id,
    target_id: job.target_id ?? job.stack_id,
    error: job.error,
    payload: job.payload,
    result: job.result,
    step: job.step,
    current_step: job.current_step,
    message: job.message,
    error_details: job.error_details,
    logs: job.logs,
    wait_reason: job.wait_reason,
    next_resume_at: job.next_resume_at,
    resume_available_at: job.resume_available_at,
    resume_available: job.resume_available,
    created_at: job.created_at ?? job.created ?? "",
    updated_at: job.updated_at ?? job.updated ?? "",
  };
}

// ============================================================================
// API Functions
// ============================================================================

/**
 * Get list of jobs
 */
export async function getJobs(): Promise<Job[]> {
  return (await listJobs({ limit: 50 })).items;
}

export async function listJobs(
  request: JobsListRequest = {},
): Promise<JobsListResult> {
  const params = new URLSearchParams();
  if (request.page) params.set("page", String(request.page));
  if (request.perPage) params.set("per_page", String(request.perPage));
  if (request.limit) params.set("limit", String(request.limit));
  if (request.stackId) params.set("stack_id", request.stackId);
  if (request.state) params.set("state", request.state);
  if (request.type) params.set("type", request.type);
  if (request.search) params.set("search", request.search);

  const suffix = params.toString() ? `?${params.toString()}` : "";
  const res = await fetchApi<JobsListApiResponse>(`/api/v1/jobs${suffix}`, {
    method: "GET",
    credentials: "include",
  });
  const data = res.data;
  const items = (data.items ?? []).map(normalizeJobDetail);
  return {
    items,
    page: data.page ?? request.page ?? 1,
    perPage: data.per_page ?? request.perPage ?? request.limit ?? 50,
    totalItems: data.total_items ?? items.length,
    totalPages: data.total_pages ?? (items.length > 0 ? 1 : 0),
  };
}

export async function getLatestStackProvisionJob(
  stackId: string,
): Promise<JobDetail | null> {
  if (!stackId) return null;
  const result = await listJobs({
    stackId,
    type: "provision",
    page: 1,
    perPage: 1,
  });
  return result.items[0] ?? null;
}

/**
 * Get job details by ID
 */
export async function getJob(id: string): Promise<JobDetail> {
  const encoded = encodeURIComponent(id);
  const res = await fetchApi<JobDetailApiResponse>(`/api/v1/jobs/${encoded}`, {
    method: "GET",
    credentials: "include",
  });
  return normalizeJobDetail(res.data);
}

// ============================================================================
// Job progress streaming (SSE)
// ============================================================================

export interface JobStreamHandlers {
  /** A progress or done event arrived; re-fetch the job for the full detail. */
  onEvent: (eventName: "progress" | "done") => void;
  /**
   * The stream ended without a done event (server timeout event, transport
   * error, or auth failure). The caller should fall back to polling.
   */
  onFallback: () => void;
}

/**
 * Subscribe to GET /api/v1/jobs/{id}/stream as a change-notification channel.
 *
 * The SSE payload intentionally stays unused: it carries only the progress
 * projection, not the job result the creating page derives its UI from, so
 * every event triggers a full job fetch instead. Uses a same-origin cookie
 * EventSource (an EventSource cannot carry the SaaS gateway bearer header);
 * when that auth lane is unavailable the error handler fires and the caller
 * keeps polling.
 *
 * Returns a close function; safe to call multiple times.
 */
export function streamJobProgress(
  jobId: string,
  handlers: JobStreamHandlers,
): () => void {
  let source: EventSource | null = null;
  let ended = false;
  const close = () => {
    if (source) {
      source.close();
      source = null;
    }
  };
  const end = (fallback: boolean) => {
    if (ended) return;
    ended = true;
    close();
    if (fallback) handlers.onFallback();
  };
  try {
    source = new EventSource(
      `${API_BASE}/api/v1/jobs/${encodeURIComponent(jobId)}/stream`,
      { withCredentials: true },
    );
  } catch {
    handlers.onFallback();
    return () => {};
  }
  source.addEventListener("progress", () => {
    if (!ended) handlers.onEvent("progress");
  });
  source.addEventListener("done", () => {
    if (ended) return;
    handlers.onEvent("done");
    end(false);
  });
  source.addEventListener("timeout", () => end(true));
  source.addEventListener("error", () => end(true));
  source.onerror = () => end(true);
  return () => end(false);
}
