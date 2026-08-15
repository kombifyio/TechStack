/**
 * Workers API
 *
 * Functions for managing worker nodes in kombify-TechStack.
 * Workers are physical or virtual machines that run the kombify-TechStack agent.
 */

import { fetchApi } from "./client";

// ============================================================================
// Types
// ============================================================================

/**
 * Worker represents a registered node in the kombify-TechStack cluster.
 */
export interface Worker {
  id: string;
  hostname: string;
  ip: string;
  os: string;
  arch: string;
  stack_id?: string;
  connected_at?: string;
  last_seen?: string;
  status: string;
  approved?: boolean;
  approved_at?: string;
  created?: string;
  cpu_cores?: number;
  ram_mb?: number;
  disk_gb?: number;
  gpu?: string;
  has_nvme?: boolean;
  has_hw_transcode?: boolean;
  docker_version?: string;
  type?: string;
  provider?: string;
  tags?: string;
  tenant_id?: string;
  source?: "worker-registry" | "managed-runtime" | string;
  lease_id?: string;
  runtime_lane?: string;
  runtime_offering_id?: string;
  desired_state?: string;
  enrollment_status?: string;
  assignable?: boolean;
}

/**
 * Request payload for registering a new worker.
 */
export interface RegisterWorkerRequest {
  token: string;
  hostname: string;
  os: string;
  arch: string;
}

/**
 * Response from worker registration.
 */
export interface RegisterWorkerResponse {
  worker_id: string;
  accepted: boolean;
}

// ============================================================================
// API Functions
// ============================================================================

/**
 * List all registered workers.
 * Returns an empty array if no workers are found (404).
 */
export async function listWorkers(): Promise<Worker[]> {
  try {
    const res = await fetchApi<Worker[]>("/api/v1/workers");
    return res.data;
  } catch (err) {
    if (err instanceof Error && /\b404\b/.test(err.message)) {
      return [];
    }
    throw err;
  }
}

/**
 * Register a new worker node.
 * @param req - Registration request containing token, hostname, os, and arch
 * @returns Registration response with worker_id and acceptance status
 */
export async function registerWorker(
  req: RegisterWorkerRequest,
): Promise<RegisterWorkerResponse> {
  const res = await fetchApi<RegisterWorkerResponse>(
    "/api/v1/workers/register",
    {
      method: "POST",
      body: JSON.stringify(req),
    },
  );
  return res.data;
}

/**
 * Confirm a pending worker for cluster membership.
 * @param workerId - The ID of the worker to confirm
 */
export async function approveWorker(workerId: string): Promise<void> {
  await fetchApi<{ message?: string }>(`/api/v1/workers/${workerId}/approve`, {
    method: "POST",
  });
}
