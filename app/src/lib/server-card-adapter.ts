import type {
  ServerKitInfo,
  ServerMetric,
  ServerStatusKind,
} from "$lib/components/open-core";

import type { StackMetricValue, StackOperationServer } from "$lib/api/stacks";
import { isManagedRuntimeServer } from "$lib/managed-runtime-server";

export function formatMetric(metric: StackMetricValue | undefined): string {
  if (!metric || metric.value === undefined || metric.status !== "ok") {
    return "unknown";
  }
  // The agent reports a raw float, so a live card rendered
  // "CPU 1.2345678882817885%" next to a neighbour showing "0%". One decimal
  // is the most precision a utilisation reading carries meaning at, and it
  // keeps the three metric tiles the same width as the numbers move.
  const value =
    typeof metric.value === "number" && Number.isFinite(metric.value)
      ? Number(metric.value.toFixed(1))
      : metric.value;
  return `${value}${metric.unit || ""}`;
}

export function formatCapacity(
  value: number | undefined,
  suffix: string,
): string {
  if (!value || value <= 0) return "unknown";
  return `${Math.round(value)} ${suffix}`;
}

export function serverOSLabel(server: StackOperationServer): string {
  const os = server.os?.trim() || "os unknown";
  const version = server.os_version?.trim();
  const osWithVersion =
    version && !os.toLowerCase().includes(version.toLowerCase())
      ? `${os} ${version}`
      : os;
  return `${osWithVersion}/${server.arch?.trim() || "arch unknown"}`;
}

export function serverPrimaryAddress(server: StackOperationServer): string {
  const addresses = server.host_addresses || [];
  return (
    server.ip?.trim() ||
    addresses.find((address) => address.scope === "public")?.address ||
    addresses.find((address) => address.scope === "primary")?.address ||
    addresses.find((address) => address.scope === "private")?.address ||
    addresses[0]?.address ||
    "not reported"
  );
}

export function serverDomains(server: StackOperationServer): string[] {
  return Array.from(
    new Set(
      [
        ...(server.domains || []),
        ...(server.service_endpoints || []).map(
          (endpoint) => endpoint.domain || "",
        ),
      ]
        .map((domain) => domain.trim())
        .filter(Boolean),
    ),
  );
}

export function serverStackKitName(server: StackOperationServer): string {
  return (
    server.stackkit?.name?.trim() ||
    server.stackkit?.catalog_ref?.trim() ||
    "not reported"
  );
}

export function serverStackKitVariant(server: StackOperationServer): string {
  if (!server.stackkit) return "deployment unknown";
  const parts = [
    server.stackkit.version,
    server.stackkit.mode,
    server.stackkit.context,
    server.stackkit.paas,
    server.stackkit.compute_tier,
  ]
    .map((part) => part?.trim())
    .filter(Boolean);
  parts.push(server.stackkit.state || "state unknown");
  return parts.join(" · ");
}

export function serverCardStatus(
  server: StackOperationServer,
): ServerStatusKind {
  const state = (server.health?.state || "").toLowerCase();
  if (state === "healthy") return "healthy";
  if (state === "degraded" || state === "error" || state === "failed") {
    return "degraded";
  }
  if (state === "offline" || state === "stale") return "offline";
  if (state === "pending") return "pending";
  return "unknown";
}

export function serverCardMeta(server: StackOperationServer): string {
  return [
    server.capabilities?.provider,
    server.role,
    serverOSLabel(server),
    isManagedRuntimeServer(server) ? "managed runtime" : "",
  ]
    .filter(Boolean)
    .join(" · ");
}

export function serverCardMetrics(
  server: StackOperationServer,
): ServerMetric[] {
  return [
    { label: "CPU", value: formatMetric(server.health?.cpu_percent) },
    { label: "RAM", value: formatMetric(server.health?.memory_percent) },
    { label: "Disk", value: formatMetric(server.health?.disk_percent) },
  ];
}

export function serverCardKit(server: StackOperationServer): ServerKitInfo {
  return {
    name: serverStackKitName(server) || "not reported",
    detail: serverStackKitVariant(server),
  };
}
