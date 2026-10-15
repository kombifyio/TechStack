import { fetchApi } from "./client";

export interface DriveReading {
  drive_id: string;
  device_path: string;
  protocol: "ATA" | "NVMe" | "SCSI" | "";
  model: string;
  serial: string;
  wwn: string;
  source: "smartctl" | "nvme_cli" | "inventory";
  observed_at: string;
  status: string;
  reason?: string;
  missing: string[];
  smart_passed: boolean | null;
  attribute_failed: boolean | null;
  self_test_failed: boolean | null;
  temperature_celsius: number | null;
  temperature_warning_celsius: number | null;
  temperature_critical_celsius: number | null;
  wear_attributes: {
    id: number;
    name: string;
    value: number | null;
    raw: string | null;
    interpretation: "vendor_specific";
  }[];
  critical_warning: number | null;
  percentage_used: number | null;
  available_spare: number | null;
  spare_threshold: number | null;
  // Decimal counters stay strings: NVMe counters can exceed JS safe integers.
  media_errors: string | null;
  error_log_entries: string | null;
  reallocated_sectors: string | null;
  pending_sectors: string | null;
  uncorrectable_sectors: string | null;
  power_on_hours: string | null;
}

export interface DriveAlert {
  id: string;
  server_id: string;
  drive_id?: string;
  category: "collection" | "hardware";
  code: string;
  severity: string;
  evidence: Record<string, string>;
  first_seen_at: string;
  last_seen_at: string;
  resolved_at?: string;
}

export interface DriveState {
  drive_id: string;
  device_path: string;
  observation_status: string;
  last_attempt_at?: string;
  health: string;
  last_attempt: DriveReading | null;
  last_success: DriveReading | null;
  alerts: DriveAlert[];
}

export interface DriveServerState {
  server_id: string;
  worker_id: string;
  last_attempt_at: string;
  collection_status: string;
  collection_reason?: string;
  inventory_complete: boolean;
  drives: DriveState[];
  alerts: DriveAlert[];
}

export interface DriveHealthReport {
  generated_at: string;
  servers: DriveServerState[];
}

export async function getDriveHealth(
  serverId: string,
): Promise<DriveHealthReport> {
  const response = await fetchApi<DriveHealthReport>(
    `/api/v1/monitor/drive-health?server_id=${encodeURIComponent(serverId)}`,
  );
  const report = response.data;
  if (
    !report ||
    !Array.isArray(report.servers) ||
    report.servers.some(
      (server) =>
        !server ||
        typeof server.server_id !== "string" ||
        !Array.isArray(server.drives) ||
        !Array.isArray(server.alerts) ||
        server.drives.some(
          (drive) =>
            !drive ||
            typeof drive.drive_id !== "string" ||
            !Array.isArray(drive.alerts),
        ),
    )
  ) {
    throw new Error("Invalid drive health report");
  }
  return report;
}

// A successful old reading cannot establish current health after observation
// loss. Keep it for evidence while the current summary fails closed.
export function driveCondition(
  drive: DriveState,
  server: DriveServerState,
  unavailable = false,
  disconnected = false,
  now = Date.now(),
): string {
  if (disconnected) return "disconnected";
  if (unavailable) return "unknown";
  const statuses = [
    "unsupported",
    "inaccessible",
    "missing",
    "incomplete",
    "stale",
    "disconnected",
  ];
  if (statuses.includes(server.collection_status))
    return server.collection_status;
  if (statuses.includes(drive.observation_status))
    return drive.observation_status;
  const observed = drive.last_success;
  if (!observed || observed.drive_id !== drive.drive_id) return "missing";
  if (observed.status !== "available" || !server.inventory_complete)
    return "incomplete";
  const at = Date.parse(observed.observed_at);
  if (!Number.isFinite(at)) return "unknown";
  if (at > now + 5 * 60 * 1000) return "unknown";
  if (now - at > 30 * 60 * 1000) return "stale";
  if (
    server.collection_status !== "available" ||
    drive.observation_status !== "available"
  )
    return "unknown";
  return ["healthy", "warning", "critical"].includes(drive.health)
    ? drive.health
    : "unknown";
}
