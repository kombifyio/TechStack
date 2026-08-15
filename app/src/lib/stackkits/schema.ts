/**
 * StackKit TypeScript Schema
 *
 * This file provides TypeScript types derived from the CUE schemas.
 * It's used by the Wizard for form generation and client-side validation.
 *
 * NOTE: This file should be regenerated when CUE schemas change.
 * Future: Automate via `make generate-ts` from CUE definitions.
 */

// =============================================================================
// CORE TYPES
// =============================================================================

/** Available StackKit identifiers (directory names use -kit suffix) */
export type StackKitRef = "basement-kit" | "cloud-kit";

/** Node types */
export type NodeType = "main" | "worker" | "edge";

/** Infrastructure providers */
export type Provider =
  | "local"
  | "hetzner"
  | "docker"
  | "aws"
  | "gcp"
  | "azure"
  | "digitalocean"
  | "digitalocean-managed"
  | "centron"
  | "ionos";

/** Service types */
export type ServiceType =
  | "reverse-proxy"
  | "database"
  | "cache"
  | "queue"
  | "monitoring"
  | "backup"
  | "auth"
  | "storage"
  | "media"
  | "automation"
  | "development"
  | "custom";

/** Service exposure levels */
export type ServiceExposure = "internal" | "vpn" | "public";

/** VPN types */
export type VPNType = "none" | "wireguard" | "headscale" | "tailscale";

// =============================================================================
// SYSTEM CONFIGURATION
// =============================================================================

export interface SystemConfig {
  timezone?: string; // Default: "UTC"
  locale?: string; // Default: "en_US.UTF-8"
  swap?: "disabled" | "enabled"; // Default: "disabled"
  unattendedUpgrades?: "disabled" | "security" | "all"; // Default: "security"
}

// =============================================================================
// SECURITY CONFIGURATION
// =============================================================================

export interface SSHConfig {
  host: string;
  port?: number; // Default: 22
  user?: string; // Default: "ubuntu"
  keyPath?: string;
}

export interface SSHHardening {
  passwordAuth?: boolean; // Default: false
  permitRootLogin?: "yes" | "no" | "prohibit-password"; // Default: "no"
  maxAuthTries?: number; // Default: 3
  port?: number; // Default: 22
  allowUsers?: string[]; // Default: ["ks-admin"]
}

export interface Fail2banConfig {
  enabled?: boolean; // Default: true
  maxRetry?: number; // Default: 5
  banTime?: string; // Default: "1h"
  findTime?: string; // Default: "10m"
}

export interface FirewallRule {
  port: number | string; // Port number or range (e.g., "8000:9000")
  protocol?: "tcp" | "udp" | "icmp"; // Default: "tcp"
  source: "any" | "lan" | "vpn" | string; // CIDR notation for specific IPs
  direction?: "in" | "out"; // Default: "in"
  action?: "allow" | "deny"; // Default: "allow"
  comment?: string;
}

export interface FirewallPolicy {
  defaultInbound?: "allow" | "deny"; // Default: "deny"
  defaultOutbound?: "allow" | "deny"; // Default: "allow"
  backend?: "iptables" | "nftables" | "firewalld" | "ufw"; // Default: "ufw"
  rules?: FirewallRule[];
}

export interface TLSPolicy {
  minVersion?: "1.2" | "1.3"; // Default: "1.2"
  requireTLS?: boolean; // Default: true
  certSource?: "acme" | "self-signed" | "manual"; // Default: "acme"
  acmeProvider?: "letsencrypt" | "zerossl" | "buypass";
  acmeEmail?: string;
}

export interface SecurityConfig {
  ssh?: SSHHardening;
  firewall?: FirewallPolicy;
  tls?: TLSPolicy;
}

// =============================================================================
// NETWORK CONFIGURATION
// =============================================================================

export interface DNSConfig {
  servers?: string[]; // Default: ["1.1.1.1", "8.8.8.8"]
  searchDomains?: string[];
  localCache?: boolean; // Default: true
}

export interface VPNConfig {
  type?: VPNType; // Default: "none"
  interface?: string; // Default: "wg0"
  listenPort?: number; // Default: 51820
  subnet?: string; // CIDR notation
}

export interface ProxySettings {
  domain: string;
  pathPrefix?: string; // Default: "/"
  websocket?: boolean; // Default: false
}

export interface NetworkConfig {
  vpn?: VPNType;
  domain?: string; // Default: "home.local"
  dns?: DNSConfig;
}

// =============================================================================
// OBSERVABILITY CONFIGURATION
// =============================================================================

export interface LoggingConfig {
  driver?: "journald" | "json-file" | "syslog"; // Default: "journald"
  format?: "json" | "text"; // Default: "json"
  retention?: string; // Default: "7d"
  level?: "debug" | "info" | "warn" | "error"; // Default: "info"
}

export interface HealthCheck {
  enabled?: boolean; // Default: true
  interval?: string; // Default: "30s"
  timeout?: string; // Default: "10s"
  retries?: number; // Default: 3
  startPeriod?: string; // Default: "60s"
  type?: "http" | "tcp" | "exec"; // Default: "http"
  path?: string; // Default: "/health" (for HTTP)
  status?: number; // Default: 200 (for HTTP)
}

export interface MetricsConfig {
  enabled?: boolean; // Default: false
  port?: number; // Default: 9100
  scrapeInterval?: string; // Default: "15s"
  nodeExporter?: {
    enabled?: boolean;
    port?: number;
  };
}

export interface BackupConfig {
  enabled?: boolean; // Default: false
  schedule?: string; // Default: "0 3 * * *"
  retention?: string; // Default: "7d"
  target?: {
    type: "local" | "s3" | "restic";
    path?: string; // For local
    bucket?: string; // For S3
    region?: string; // For S3
  };
}

export interface ObservabilityConfig {
  logging?: LoggingConfig;
  health?: HealthCheck;
  metrics?: MetricsConfig;
  backup?: BackupConfig;
}

// =============================================================================
// NODE DEFINITION
// =============================================================================

export interface ResourceSpec {
  cpu?: number; // Default: 2
  memory?: string; // Default: "2Gi" (e.g., "512Mi", "4Gi")
  disk?: string; // Default: "20Gi" (e.g., "100Gi", "1Ti")
}

export interface NodeDefinition {
  name: string; // DNS-compatible, e.g., "homeserver"
  type: NodeType;
  provider: Provider;
  ssh?: SSHConfig; // Required for non-docker providers
  resources?: ResourceSpec;
  labels?: Record<string, string>;
}

// =============================================================================
// SERVICE DEFINITION
// =============================================================================

export interface PortMapping {
  container: number;
  host?: number;
  protocol?: "tcp" | "udp";
  exposure?: ServiceExposure;
  bindAddress?: string; // Default: all interfaces
}

export interface VolumeMount {
  name: string;
  mountPath: string;
  hostPath?: string;
  readOnly?: boolean;
  type?: "bind" | "volume" | "tmpfs";
  backup?: boolean;
}

export interface ServiceNetwork {
  exposure?: ServiceExposure; // Default: "internal"
  ports?: PortMapping[];
  proxy?: ProxySettings;
  aliases?: string[];
}

export interface ServiceDefinition {
  name: string; // DNS-compatible
  type: ServiceType;
  displayName?: string;
  description?: string;
  node?: string; // Target node name
  needs?: string[]; // Service dependencies
  image?: string; // Container image
  tag?: string; // Image tag
  network?: ServiceNetwork;
  healthCheck?: HealthCheck;
  resources?: ResourceSpec;
  environment?: Record<string, string>;
  volumes?: VolumeMount[];
  config?: Record<string, unknown>; // Service-specific config
  restartPolicy?: "always" | "unless-stopped" | "on-failure" | "no";
}

// =============================================================================
// KOMBINATION SPEC (Root Schema)
// =============================================================================

export interface KombinationSpec {
  // Required fields
  name: string; // DNS-compatible stack name
  kit: StackKitRef; // StackKit to use
  nodes: NodeDefinition[]; // At least one node

  // Optional fields
  version?: string; // Default: "1.0"
  services?: ServiceDefinition[];
  system?: SystemConfig;
  security?: SecurityConfig;
  network?: NetworkConfig;
  observability?: ObservabilityConfig;
  metadata?: Record<string, string>;
}

// =============================================================================
// STACKKIT METADATA
// =============================================================================

export interface StackKitMetadata {
  name: StackKitRef;
  displayName: string;
  version: string;
  description: string;
  author?: string;
  license?: string;
  tags?: string[];
  minTechstackVersion?: string;
  deprecated?: boolean;
}

// =============================================================================
// AVAILABLE STACKKITS (for Wizard selection)
// =============================================================================

export const AVAILABLE_STACKKITS: StackKitMetadata[] = [
  {
    name: "basement-kit",
    displayName: "Basement Kit",
    version: "0.5.0",
    description:
      "User-owned or local StackKit runtime for self-registered servers",
    tags: ["local", "user-owned", "single-node", "docker"],
    minTechstackVersion: "0.5.0",
  },
  {
    name: "cloud-kit",
    displayName: "Cloud Kit",
    version: "0.5.0",
    description:
      "Managed VPS StackKit runtime for kombify-managed cloud servers",
    tags: ["managed-vps", "cloud", "kombify-managed", "docker"],
    minTechstackVersion: "0.5.0",
  },
];

// =============================================================================
// VALIDATION HELPERS
// =============================================================================

/** DNS-compatible name pattern (RFC 1123) */
export const DNS_NAME_PATTERN = /^[a-z][a-z0-9-]{0,61}[a-z0-9]$/;

/** Validate DNS-compatible name */
export function isValidDNSName(name: string): boolean {
  return DNS_NAME_PATTERN.test(name);
}

/** IP address pattern */
export const IP_ADDRESS_PATTERN = /^(\d{1,3}\.){3}\d{1,3}$/;

/** Validate IP address */
export function isValidIPAddress(ip: string): boolean {
  if (!IP_ADDRESS_PATTERN.test(ip)) return false;
  const parts = ip.split(".").map(Number);
  return parts.every((p) => p >= 0 && p <= 255);
}

/** CIDR notation pattern */
export const CIDR_PATTERN = /^(\d{1,3}\.){3}\d{1,3}\/\d{1,2}$/;

/** Memory/disk size pattern (e.g., "512Mi", "2Gi") */
export const SIZE_PATTERN = /^\d+(Mi|Gi|Ti)$/;

/** Duration pattern (e.g., "30s", "5m", "1h") */
export const DURATION_PATTERN = /^\d+[smhd]$/;

/** Cron expression pattern (basic) */
export const CRON_PATTERN = /^[0-9* ,/-]+$/;
