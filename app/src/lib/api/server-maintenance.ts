import { fetchApi } from "./client";

export type MaintenanceAction = "os_update" | "reboot";
export interface MaintenanceImpact {
  server_id: string;
  action: MaintenanceAction;
  inventory_revision: number;
  snapshot_digest: string;
  inventory: {
    status: "current" | "stale" | "incomplete" | "unavailable";
    observed_at?: string;
    reason_codes: string[];
  };
  services: {
    id: string;
    name: string;
    stack_id?: string;
    state: string;
    locked: boolean;
    reason_codes: string[];
  }[];
  stacks: { id: string; name: string; reason_codes: string[] }[];
  confirmable: boolean;
}

export interface MaintenanceRequest {
  action: MaintenanceAction | "os_update_plan";
  expected_inventory_revision: number;
  owner_approved: boolean;
  expected_plan_digest?: string;
  expected_impact_digest?: string;
}

export interface MaintenanceAccepted {
  job_id: string;
  server_id: string;
  action: MaintenanceRequest["action"];
  status: string;
}

export interface MaintenanceJob extends MaintenanceAccepted {
  state: string;
  reason_code?: string;
  plan_digest?: string;
  result: {
    plan_digest?: string;
    pending_count?: number;
    security_count?: number;
    reboot_likely?: boolean;
    reboot_required?: boolean;
    packages?: { name: string; from: string; to: string; security: boolean }[];
    packages_truncated?: boolean;
    dpkg_problems?: string[];
    held?: string[];
  };
  failure?: {
    code: string;
    retryable: boolean;
    user_guidance: { title: string; body: string; next_steps: string[] };
  };
  reboot?: { boot_id_changed: boolean; return_deadline?: string };
  completed_at?: string;
}

function path(serverId: string): string {
  return `/api/v1/servers/${encodeURIComponent(serverId)}`;
}

export async function getMaintenanceImpact(
  serverId: string,
  action: MaintenanceAction,
): Promise<MaintenanceImpact> {
  return (
    await fetchApi<MaintenanceImpact>(
      `${path(serverId)}/maintenance-impact?action=${action}`,
    )
  ).data;
}

export async function requestMaintenance(
  serverId: string,
  request: MaintenanceRequest,
  idempotencyKey: string,
): Promise<MaintenanceAccepted> {
  return (
    await fetchApi<MaintenanceAccepted>(`${path(serverId)}/actions`, {
      method: "POST",
      headers: { "Idempotency-Key": idempotencyKey },
      body: JSON.stringify(request),
    })
  ).data;
}

export async function getMaintenanceJob(
  serverId: string,
  jobId: string,
): Promise<MaintenanceJob> {
  return (
    await fetchApi<MaintenanceJob>(
      `${path(serverId)}/actions/${encodeURIComponent(jobId)}`,
    )
  ).data;
}

export async function cancelMaintenanceJob(
  serverId: string,
  jobId: string,
): Promise<void> {
  await fetchApi(
    `${path(serverId)}/actions/${encodeURIComponent(jobId)}/cancel`,
    { method: "POST" },
  );
}
