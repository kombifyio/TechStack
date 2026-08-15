/**
 * kombify-TechStack Discovery Store (Svelte 5 Runes)
 *
 * Reactive state management for network discovery.
 */

import type {
  DiscoveredDevice,
  NetworkInterface,
  ScanResult,
  ScanRequest,
  ProbeRequest,
  SSHTestRequest,
} from "./types";
import * as api from "./api";
import { DiscoveryApiError } from "./api";

/**
 * Scan phase for detailed progress tracking
 */
export type ScanPhase =
  | "idle"
  | "connecting"
  | "loading-networks"
  | "starting-scan"
  | "waiting-response"
  | "polling"
  | "processing"
  | "complete"
  | "failed"
  | "canceled";

/**
 * Detailed scan status for UI feedback
 */
export interface ScanStatus {
  phase: ScanPhase;
  message: string;
  detail?: string;
  startedAt?: Date;
  elapsedMs?: number;
}

/**
 * Discovery store state
 */
interface DiscoveryState {
  // Networks
  networks: NetworkInterface[];
  networksLoading: boolean;
  networksError: string | null;

  // Scan
  currentScan: ScanResult | null;
  isScanning: boolean;
  scanError: string | null;
  scancanceled: boolean;
  scanStatus: ScanStatus;

  // Devices
  devices: DiscoveredDevice[];
  selectedDeviceIds: Set<string>;

  // Probe
  probingDeviceId: string | null;
  probeError: string | null;
}

/**
 * Create a discovery store instance.
 */
export function createDiscoveryStore() {
  // State using Svelte 5 $state rune
  let state = $state<DiscoveryState>({
    networks: [],
    networksLoading: false,
    networksError: null,
    currentScan: null,
    isScanning: false,
    scanError: null,
    scancanceled: false,
    scanStatus: { phase: "idle", message: "Ready to scan" },
    devices: [],
    selectedDeviceIds: new Set(),
    probingDeviceId: null,
    probeError: null,
  });

  // Helper to update scan status
  function updateScanStatus(
    phase: ScanPhase,
    message: string,
    detail?: string,
  ) {
    const elapsed = state.scanStatus.startedAt
      ? Date.now() - state.scanStatus.startedAt.getTime()
      : undefined;
    state.scanStatus = {
      phase,
      message,
      detail,
      startedAt: state.scanStatus.startedAt,
      elapsedMs: elapsed,
    };
  }

  // Derived values
  const selectedDevices = $derived(
    state.devices.filter((d) => state.selectedDeviceIds.has(d.device_id)),
  );

  const hasSelection = $derived(state.selectedDeviceIds.size > 0);

  const scanProgress = $derived(state.currentScan?.progress ?? 0);

  const deviceCount = $derived(state.devices.length);

  // Actions
  async function loadNetworks() {
    state.networksLoading = true;
    state.networksError = null;

    try {
      state.networks = await api.getNetworks();
    } catch (err) {
      state.networksError =
        err instanceof Error ? err.message : "Failed to load networks";
    } finally {
      state.networksLoading = false;
    }
  }

  async function startScan(request?: ScanRequest) {
    state.isScanning = true;
    state.scanError = null;
    state.currentScan = null;
    state.scancanceled = false;
    state.scanStatus = {
      phase: "connecting",
      message: "Connecting to discovery service...",
      detail: `POST ${api.getApiBase()}/api/v1/discovery/scan`,
      startedAt: new Date(),
    };

    try {
      // Phase 1: Check if backend is reachable
      updateScanStatus(
        "loading-networks",
        "Checking network interfaces...",
        "Querying local network configuration",
      );

      // Load networks first to ensure connectivity
      if (state.networks.length === 0) {
        try {
          state.networks = await api.getNetworks();
        } catch (netErr) {
          throw new DiscoveryApiError(
            `Cannot reach discovery service. Is the backend running? (${netErr instanceof Error ? netErr.message : "Unknown error"})`,
            0,
          );
        }
      }

      const networkInfo =
        state.networks.length > 0
          ? `Found ${state.networks.length} interface(s): ${state.networks.map((n) => n.subnets?.join(", ")).join("; ")}`
          : "No networks detected";

      updateScanStatus(
        "starting-scan",
        "Starting network scan...",
        networkInfo,
      );

      // Phase 2: Start the scan - this may take a LONG time for large subnets
      updateScanStatus(
        "waiting-response",
        "Scanning network (this may take 1-2 minutes for large subnets)...",
        `Profile: ${request?.profile || "active"} | Subnet: ${request?.subnet || "auto-detect"}`,
      );

      const result = await api.scanAndWait(request, (progress) => {
        // Check if canceled
        if (state.scancanceled) {
          throw new Error("Scan canceled by user");
        }
        state.currentScan = progress;

        // Update status based on scan progress
        const progressPct = progress.progress || 0;
        const scanned = progress.scanned_hosts || 0;
        const total = progress.total_hosts || 0;
        const found = progress.devices?.length || 0;

        if (progress.status === "running") {
          updateScanStatus(
            "polling",
            `Scanning: ${progressPct}% complete`,
            `Scanned ${scanned}/${total} hosts | Found ${found} device(s)`,
          );
        }

        // Update devices as they're discovered
        if (progress.devices) {
          state.devices = progress.devices;
        }
      });

      // If canceled during scan, don't update final result
      if (state.scancanceled) {
        updateScanStatus(
          "canceled",
          "Scan canceled",
          "User requested cancellation",
        );
        return;
      }

      state.currentScan = result;
      state.devices = result.devices || [];

      if (result.status === "failed") {
        const errorMsg = result.errors?.join(", ") || "Scan failed";
        state.scanError = errorMsg;
        updateScanStatus("failed", "Scan failed", errorMsg);
      } else {
        updateScanStatus(
          "complete",
          `Scan complete: ${result.devices?.length || 0} device(s) found`,
          `Scanned ${result.scanned_hosts || 0} hosts in ${result.subnet || "local network"}`,
        );
      }
    } catch (err) {
      if (!state.scancanceled) {
        let errorMsg: string;
        let errorDetail: string | undefined;

        if (err instanceof DiscoveryApiError) {
          errorMsg = err.message;
          errorDetail = err.status ? `HTTP ${err.status}` : "Network error";
        } else if (err instanceof TypeError && err.message.includes("fetch")) {
          errorMsg = "Cannot connect to discovery service";
          errorDetail =
            "The backend server may not be running. Check that docker-compose is up.";
        } else if (err instanceof Error) {
          if (
            err.message.includes("timeout") ||
            err.message.includes("Timeout")
          ) {
            errorMsg = "Scan timed out";
            errorDetail =
              "The network scan took too long. Try scanning a smaller subnet or use passive mode.";
          } else {
            errorMsg = err.message;
          }
        } else {
          errorMsg = "Scan failed";
        }

        state.scanError = errorMsg;
        updateScanStatus("failed", errorMsg, errorDetail);
      }
    } finally {
      state.isScanning = false;
    }
  }

  async function cancelScan() {
    state.scancanceled = true;
    state.isScanning = false;
    state.scanError = "Scan canceled by user";
    state.scanStatus = {
      phase: "canceled",
      message: "Scan canceled",
      detail: "User requested cancellation",
    };

    try {
      const scanId = state.currentScan?.scan_id;
      if (scanId) {
        await api.cancelScan(scanId);
      }
    } catch (err) {
      console.warn("Failed to cancel scan on backend:", err);
    }
  }

  async function loadDevices() {
    try {
      state.devices = await api.getDevices();
    } catch (err) {
      console.error("Failed to load devices:", err);
    }
  }

  async function probeDevice(
    deviceId: string,
    sshCredentials: Omit<ProbeRequest, "ip">,
  ) {
    const device = state.devices.find((d) => d.device_id === deviceId);
    if (!device) return;

    state.probingDeviceId = deviceId;
    state.probeError = null;

    try {
      const result = await api.probeDevice({
        ip: device.ip,
        ...sshCredentials,
      });

      // Update device in list with probe results
      const idx = state.devices.findIndex((d) => d.device_id === deviceId);
      if (idx >= 0 && result.system) {
        state.devices[idx] = {
          ...state.devices[idx],
          system: result.system,
          probe_status: result.status,
          probe_error: result.error,
        };
      }

      if (result.status === "failed") {
        state.probeError = result.error || "Probe failed";
      }
    } catch (err) {
      state.probeError = err instanceof Error ? err.message : "Probe failed";
    } finally {
      state.probingDeviceId = null;
    }
  }

  async function testSSH(request: SSHTestRequest) {
    try {
      const result = await api.testSSH(request);
      return result;
    } catch (err) {
      return {
        success: false,
        error: err instanceof Error ? err.message : "SSH test failed",
      };
    }
  }

  // Trigger on-demand enrichment (ARP cache, etc.) for the current scan
  async function enrichCurrentScan() {
    const scanId = state.currentScan?.scan_id;
    if (!scanId) return;

    state.scanStatus = {
      phase: "processing",
      message: "Running ARP enrichment...",
    };

    try {
      const result = await api.enrichScan(scanId);
      state.currentScan = result;
      state.devices = result.devices || [];
      updateScanStatus(
        result.status === "completed" ? "complete" : "processing",
        "Enrichment complete",
        `${result.devices?.length || 0} device(s) now have MAC/metadata`,
      );
    } catch (err) {
      updateScanStatus(
        "failed",
        "Enrichment failed",
        err instanceof Error ? err.message : "Failed",
      );
    }
  }

  function selectDevice(deviceId: string) {
    state.selectedDeviceIds = new Set([...state.selectedDeviceIds, deviceId]);
  }

  function deselectDevice(deviceId: string) {
    const newSet = new Set(state.selectedDeviceIds);
    newSet.delete(deviceId);
    state.selectedDeviceIds = newSet;
  }

  function toggleDevice(deviceId: string) {
    if (state.selectedDeviceIds.has(deviceId)) {
      deselectDevice(deviceId);
    } else {
      selectDevice(deviceId);
    }
  }

  function selectSingle(deviceId: string) {
    state.selectedDeviceIds = new Set([deviceId]);
  }

  function clearSelection() {
    state.selectedDeviceIds = new Set();
  }

  function selectAll() {
    state.selectedDeviceIds = new Set(state.devices.map((d) => d.device_id));
  }

  async function clearCache() {
    try {
      await api.clearCache();
      state.devices = [];
      state.currentScan = null;
      state.selectedDeviceIds = new Set();
    } catch (err) {
      console.error("Failed to clear cache:", err);
    }
  }

  function reset() {
    state.networks = [];
    state.networksLoading = false;
    state.networksError = null;
    state.currentScan = null;
    state.isScanning = false;
    state.scanError = null;
    state.scancanceled = false;
    state.scanStatus = { phase: "idle", message: "Ready to scan" };
    state.devices = [];
    state.selectedDeviceIds = new Set();
    state.probingDeviceId = null;
    state.probeError = null;
  }

  return {
    // State (readonly getters)
    get networks() {
      return state.networks;
    },
    get networksLoading() {
      return state.networksLoading;
    },
    get networksError() {
      return state.networksError;
    },
    get currentScan() {
      return state.currentScan;
    },
    get isScanning() {
      return state.isScanning;
    },
    get scanError() {
      return state.scanError;
    },
    get devices() {
      return state.devices;
    },
    get selectedDeviceIds() {
      return state.selectedDeviceIds;
    },
    get probingDeviceId() {
      return state.probingDeviceId;
    },
    get probeError() {
      return state.probeError;
    },
    get scanStatus() {
      return state.scanStatus;
    },

    // Derived
    get selectedDevices() {
      return selectedDevices;
    },
    get hasSelection() {
      return hasSelection;
    },
    get scanProgress() {
      return scanProgress;
    },
    get deviceCount() {
      return deviceCount;
    },
    get scancanceled() {
      return state.scancanceled;
    },

    // Actions
    loadNetworks,
    startScan,
    cancelScan,
    loadDevices,
    probeDevice,
    testSSH,
    enrichCurrentScan,
    selectDevice,
    deselectDevice,
    toggleDevice,
    selectSingle,
    clearSelection,
    selectAll,
    clearCache,
    reset,
  };
}

/**
 * Singleton store instance for app-wide use.
 */
let _discoveryStore: ReturnType<typeof createDiscoveryStore> | null = null;

export function getDiscoveryStore() {
  if (!_discoveryStore) {
    _discoveryStore = createDiscoveryStore();
  }
  return _discoveryStore;
}
