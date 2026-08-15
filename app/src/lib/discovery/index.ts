/**
 * kombify-TechStack Discovery Module
 *
 * Re-exports all discovery-related types and functions.
 */

// Types
export type {
  ScanProfile,
  ProbeStatus,
  ScanStatus,
  DeviceRole,
  DiscoveredService,
  SystemInfo,
  DiscoveredDevice,
  ScanRequest,
  ScanResult,
  ProbeRequest,
  ProbeResult,
  NetworkInterface,
  SSHTestRequest,
  SSHTestResult,
  DiscoveryStats,
} from "./types";

// Type helpers
export {
  getRoleDisplayName,
  getRoleIcon,
  getProbeStatusDisplay,
  getScanStatusDisplay,
  formatConfidence,
  getConfidenceColor,
  hasSSH,
  hasDocker,
  getPrimaryRole,
} from "./types";

// API functions
export {
  getNetworks,
  startScan,
  getScanStatus,
  getDevices,
  getDevice,
  probeDevice,
  testSSH,
  clearCache,
  getStats,
  pollScanUntilComplete,
  scanAndWait,
  DiscoveryApiError,
} from "./api";

// Store
export {
  createDiscoveryStore,
  getDiscoveryStore,
  type ScanPhase,
  type ScanStatus as StoreScanStatus,
} from "./store.svelte";
