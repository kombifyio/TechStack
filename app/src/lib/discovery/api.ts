/**
 * kombify-TechStack Discovery API Client
 *
 * Handles communication with the Discovery API endpoints.
 */

import type {
  DiscoveredDevice,
  DiscoveryStats,
  NetworkInterface,
  ProbeRequest,
  ProbeResult,
  ScanRequest,
  ScanResult,
  SSHTestRequest,
  SSHTestResult,
} from "./types";

const API_BASE =
  import.meta.env.VITE_API_URL ||
  (typeof process !== "undefined" && process.env
    ? process.env.TECHSTACK_API_URL || ""
    : "");

/**
 * Get the API base URL (for debugging/display)
 */
export function getApiBase(): string {
  return API_BASE;
}

/**
 * Custom error class for Discovery API errors.
 */
export class DiscoveryApiError extends Error {
  constructor(
    message: string,
    public status?: number,
    public details?: unknown,
  ) {
    super(message);
    this.name = "DiscoveryApiError";
  }
}

/**
 * Internal fetch wrapper for Discovery API.
 */
async function fetchDiscovery<T>(
  endpoint: string,
  options: RequestInit = {},
): Promise<T> {
  if (typeof window === "undefined" && !API_BASE) {
    throw new Error(
      "Server-side API base URL is not configured. Set TECHSTACK_API_URL (runtime) or VITE_API_URL (build time).",
    );
  }

  const prefix = API_BASE ? `${API_BASE}` : "";
  const url = `${prefix}/api/v1/discovery${endpoint}`;

  let response: Response;
  try {
    response = await fetch(url, {
      ...options,
      headers: {
        "Content-Type": "application/json",
        ...options.headers,
      },
    });
  } catch (err) {
    throw new DiscoveryApiError(
      `Network error: Could not connect to Discovery API. Is the backend running?`,
    );
  }

  if (!response.ok) {
    let errorMessage = `HTTP ${response.status}`;
    let details: unknown = undefined;
    try {
      const error = await response.json();
      errorMessage =
        error?.error?.message ||
        error?.message ||
        (typeof error?.error === "string" ? error.error : "") ||
        errorMessage;
      details = error?.error?.details;
    } catch {
      errorMessage = `${response.status} ${response.statusText}`;
    }
    throw new DiscoveryApiError(errorMessage, response.status, details);
  }

  const json = await response.json();
  // Back-compat: some endpoints may return raw JSON instead of { data: ... }.
  if (json && typeof json === "object" && "data" in json) {
    return (json as any).data as T;
  }
  return json as T;
}

/**
 * Get local network interfaces and subnets.
 */
export async function getNetworks(): Promise<NetworkInterface[]> {
  const result = await fetchDiscovery<{ networks: NetworkInterface[] }>(
    "/networks",
  );
  return result.networks;
}

/**
 * Start a network discovery scan.
 */
export async function startScan(request?: ScanRequest): Promise<ScanResult> {
  return fetchDiscovery<ScanResult>("/scan", {
    method: "POST",
    body: JSON.stringify(request || {}),
  });
}

/**
 * Get the status of a running or completed scan.
 */
export async function getScanStatus(scanId: string): Promise<ScanResult> {
  return fetchDiscovery<ScanResult>(`/scan/${scanId}`);
}

/**
 * Get all discovered devices from cache.
 */
export async function getDevices(): Promise<DiscoveredDevice[]> {
  const result = await fetchDiscovery<{
    devices: DiscoveredDevice[];
    count: number;
  }>("/devices");
  return result.devices || [];
}

/**
 * Get a specific discovered device by ID.
 */
export async function getDevice(deviceId: string): Promise<DiscoveredDevice> {
  return fetchDiscovery<DiscoveredDevice>(`/devices/${deviceId}`);
}

/**
 * Perform deep discovery probe on a specific device via SSH.
 */
export async function probeDevice(request: ProbeRequest): Promise<ProbeResult> {
  return fetchDiscovery<ProbeResult>("/probe", {
    method: "POST",
    body: JSON.stringify(request),
  });
}

/**
 * Test SSH connectivity to a device.
 */
export async function testSSH(request: SSHTestRequest): Promise<SSHTestResult> {
  return fetchDiscovery<SSHTestResult>("/test-ssh", {
    method: "POST",
    body: JSON.stringify(request),
  });
}

/**
 * Clear the discovery cache.
 */
export async function clearCache(): Promise<void> {
  await fetchDiscovery<{ success: boolean }>("/cache", {
    method: "DELETE",
  });
}

/**
 * Get discovery service statistics.
 */
export async function getStats(): Promise<DiscoveryStats> {
  return fetchDiscovery<DiscoveryStats>("/stats");
}

/**
 * Poll scan status until completion.
 * @param scanId - The scan ID to poll
 * @param onProgress - Callback for progress updates
 * @param intervalMs - Polling interval in milliseconds (default: 1000)
 * @param maxAttempts - Maximum polling attempts (default: 120)
 */
export async function pollScanUntilComplete(
  scanId: string,
  onProgress?: (result: ScanResult) => void,
  intervalMs = 1000,
  maxAttempts = 120,
): Promise<ScanResult> {
  let attempts = 0;

  while (attempts < maxAttempts) {
    const result = await getScanStatus(scanId);

    if (onProgress) {
      onProgress(result);
    }

    if (
      result.status === "completed" ||
      result.status === "failed" ||
      result.status === "canceled"
    ) {
      return result;
    }

    await new Promise((resolve) => setTimeout(resolve, intervalMs));
    attempts++;
  }

  throw new DiscoveryApiError("Scan polling timeout");
}

/**
 * Convenience function: Start scan and wait for completion.
 */
export async function scanAndWait(
  request?: ScanRequest,
  onProgress?: (result: ScanResult) => void,
): Promise<ScanResult> {
  const scanResult = await startScan(request);

  // If scan already completed (fast), return immediately
  if (scanResult.status === "completed" || scanResult.status === "failed") {
    return scanResult;
  }

  // Poll for completion
  return pollScanUntilComplete(scanResult.scan_id, onProgress);
}

/**
 * Cancel a running scan
 */
export async function cancelScan(scanId: string): Promise<boolean> {
  if (typeof window === "undefined" && !API_BASE) {
    throw new Error(
      "Server-side API base URL is not configured. Set TECHSTACK_API_URL (runtime) or VITE_API_URL (build time).",
    );
  }
  const prefix = API_BASE ? `${API_BASE}` : "";
  const url = `${prefix}/api/v1/discovery/scan/${encodeURIComponent(scanId)}/cancel`;
  const res = await fetch(url, { method: "POST" });
  if (!res.ok) {
    return false;
  }
  const json = await res.json();
  return Boolean(json.canceled);
}

/**
 * Enrich an existing scan (e.g., perform ARP cache enrichment) and return updated result.
 */
export async function enrichScan(scanId: string): Promise<ScanResult> {
  return fetchDiscovery<ScanResult>(
    `/scan/${encodeURIComponent(scanId)}/enrich`,
    {
      method: "POST",
    },
  );
}
