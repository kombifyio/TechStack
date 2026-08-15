/**
 * kombify-TechStack Network Discovery Types
 *
 * TypeScript definitions for network discovery functionality.
 * Mirrors the Go types in pkg/discovery/types.go
 */

/**
 * Scan profile determines the depth of network scanning.
 */
export type ScanProfile = "passive" | "active" | "deep";

/**
 * Probe status indicates the current state of deep discovery for a device.
 */
export type ProbeStatus = "pending" | "probing" | "done" | "failed" | "skipped";

/**
 * Scan status indicates the current state of a discovery scan.
 */
export type ScanStatus =
  | "pending"
  | "running"
  | "completed"
  | "failed"
  | "canceled";

/**
 * Device role represents suggested roles for discovered devices.
 */
export type DeviceRole =
  | "main"
  | "worker"
  | "storage"
  | "utility"
  | "gateway"
  | "unknown";

/**
 * Discovered service represents a detected network service.
 */
export interface DiscoveredService {
  name: string;
  port: number;
  version?: string;
  banner?: string;
}

/**
 * System info contains hardware and OS information from deep discovery.
 */
export interface SystemInfo {
  os?: string;
  distribution?: string;
  version?: string;
  kernel?: string;
  arch?: string;
  cpu_cores?: number;
  memory_mb?: number;
  disk_gb?: number;
  docker_status?: "installed" | "running" | "not_installed";
}

/**
 * Discovered device represents a device found during network discovery.
 */
export interface DiscoveredDevice {
  device_id: string;
  ip: string;
  mac?: string;
  hostname?: string;
  services?: DiscoveredService[];
  ports?: number[];
  system?: SystemInfo;
  roles_suggested?: DeviceRole[];
  confidence: number;
  discovery_profile: ScanProfile;
  probe_status: ProbeStatus;
  probe_error?: string;
  first_seen: string;
  last_seen: string;
  metadata?: Record<string, string>;
}

/**
 * Scan request defines parameters for initiating a network scan.
 */
export interface ScanRequest {
  profile?: ScanProfile;
  subnet?: string;
  target_ips?: string[];
  exclude_ips?: string[];
  timeout_seconds?: number;
  max_concurrent?: number;
  // Experimental flags
  detect_tailscale?: boolean;
  mdns?: boolean;
}

/**
 * Scan result contains the results of a discovery scan.
 */
export interface ScanResult {
  scan_id: string;
  status: ScanStatus;
  profile: ScanProfile;
  subnet?: string;
  started_at: string;
  completed_at?: string;
  progress: number;
  total_hosts: number;
  scanned_hosts: number;
  devices: DiscoveredDevice[];
  errors?: string[];
}

/**
 * Probe request defines parameters for deep discovery of a specific device.
 */
export interface ProbeRequest {
  ip: string;
  ssh_user?: string;
  ssh_password?: string;
  ssh_private_key?: string;
  ssh_port?: number;
  timeout_seconds?: number;
}

/**
 * Probe result contains the results of a deep discovery probe.
 */
export interface ProbeResult {
  device_id: string;
  ip: string;
  status: ProbeStatus;
  system?: SystemInfo;
  error?: string;
  duration: number;
}

/**
 * Network interface represents a local network interface.
 */
export interface NetworkInterface {
  name: string;
  mac: string;
  ips: string[];
  subnets: string[];
}

/**
 * SSH test request for testing SSH connectivity.
 */
export interface SSHTestRequest {
  ip: string;
  port?: number;
  user: string;
  password?: string;
  private_key?: string;
}

/**
 * SSH test result.
 */
export interface SSHTestResult {
  success: boolean;
  error?: string;
  message?: string;
}

/**
 * Discovery service stats.
 */
export interface DiscoveryStats {
  cached_devices: number;
  active_scans: number;
  cache_expires?: string;
}

/**
 * Helper: Get role display name.
 */
export function getRoleDisplayName(role: DeviceRole): string {
  const names: Record<DeviceRole, string> = {
    main: "Main Controller",
    worker: "Worker Node",
    storage: "Storage Node",
    utility: "Utility",
    gateway: "Router/Gateway",
    unknown: "Unknown",
  };
  return names[role] || role;
}

/**
 * Helper: Get role icon (for UI).
 */
export function getRoleIcon(role: DeviceRole): string {
  const icons: Record<DeviceRole, string> = {
    main: "🎛️",
    worker: "⚙️",
    storage: "💾",
    utility: "🔧",
    gateway: "🌐",
    unknown: "❓",
  };
  return icons[role] || "❓";
}

/**
 * Helper: Get probe status display.
 */
export function getProbeStatusDisplay(status: ProbeStatus): {
  label: string;
  color: string;
} {
  const displays: Record<ProbeStatus, { label: string; color: string }> = {
    pending: { label: "Pending", color: "text-gray-400" },
    probing: { label: "Probing...", color: "text-yellow-400" },
    done: { label: "Complete", color: "text-green-400" },
    failed: { label: "Failed", color: "text-red-400" },
    skipped: { label: "Skipped", color: "text-gray-500" },
  };
  return displays[status] || { label: status, color: "text-gray-400" };
}

/**
 * Helper: Get scan status display.
 */
export function getScanStatusDisplay(status: ScanStatus): {
  label: string;
  color: string;
} {
  const displays: Record<ScanStatus, { label: string; color: string }> = {
    pending: { label: "Pending", color: "text-gray-400" },
    running: { label: "Scanning...", color: "text-primary" },
    completed: { label: "Complete", color: "text-green-400" },
    failed: { label: "Failed", color: "text-red-400" },
    canceled: { label: "canceled", color: "text-gray-500" },
  };
  return displays[status] || { label: status, color: "text-gray-400" };
}

/**
 * Helper: Format confidence as percentage.
 */
export function formatConfidence(confidence: number): string {
  return `${Math.round(confidence * 100)}%`;
}

/**
 * Helper: Get confidence color class.
 */
export function getConfidenceColor(confidence: number): string {
  if (confidence >= 0.8) return "text-green-400";
  if (confidence >= 0.5) return "text-yellow-400";
  return "text-gray-400";
}

/**
 * Helper: Check if device has SSH service.
 */
export function hasSSH(device: DiscoveredDevice): boolean {
  return (
    device.ports?.includes(22) ||
    device.services?.some((s) => s.name === "ssh") ||
    false
  );
}

/**
 * Helper: Check if device has Docker.
 */
export function hasDocker(device: DiscoveredDevice): boolean {
  return (
    device.services?.some(
      (s) => s.name === "docker" || s.name === "docker-tls",
    ) ||
    device.system?.docker_status === "running" ||
    false
  );
}

/**
 * Helper: Get primary role suggestion.
 */
export function getPrimaryRole(device: DiscoveredDevice): DeviceRole {
  return device.roles_suggested?.[0] || "unknown";
}
