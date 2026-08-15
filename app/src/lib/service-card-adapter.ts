import type {
  ServiceMetric,
  ServicePlacement,
  ServiceStatusKind,
} from "$lib/components/open-core";
import {
  ManagementStateContractError,
  type RegistryManagementState,
} from "$lib/api/registry";

export type TechStackServiceCardSource = {
  id?: string;
  name?: string;
  display_name?: string;
  type?: string;
  status?: string;
  url?: string;
  port?: number | string;
  target_server?: string;
  target_server_id?: string;
  node_name?: string;
  node_id?: string;
  server_name?: string;
  server_id?: string;
  /**
   * Optional HERE ONLY because this adapter also renders sources that have no
   * ownership dimension at all (the monitoring cockpit's
   * `StackOperationService`). Sources that DO model ownership must use
   * `CanonicalServiceCardSource`, where it is required.
   *
   * Never compare this field directly — a missing value is NOT "observed" and
   * NOT "not managed". Go through `serviceManagementState`.
   */
  management_state?: string;
  move_blocked_reason?: string;
  /**
   * An explicit placement supplied by a canonical read model.  Prefer this to
   * inferring a location from a service name: a service on a Cloud VPS is
   * still Cloud even when its Docker/container name looks local.
   */
  placement?: ServicePlacement;
  provider?: string;
  /**
   * A real, persisted workflow indicator.  A lifecycle word in `status`
   * alone is not enough to show a migration animation.
   */
  operation_state?: string;
  migration_status?: string;
  /** Whether the backend has a real migration executor for this projection. */
  migration_available?: boolean;
};

/**
 * A card source coming from a read model that guarantees the ownership
 * dimension: the canonical service aggregate (`/api/v1/services`), the
 * canonical inventory (`/api/v1/inventory/services`), and the registry BFF
 * (`/api/v1/registry/services`) all emit `management_state` non-optionally.
 */
export type CanonicalServiceCardSource = TechStackServiceCardSource & {
  management_state: RegistryManagementState;
};

/** Ownership as the UI may reason about it. `unknown` is a real answer. */
export type ServiceManagementState = RegistryManagementState | "unknown";

/**
 * Resolve the ownership dimension of a card source.
 *
 * A missing or unrecognized value resolves to `unknown` — never to `observed`
 * and never to "not managed". Collapsing the three cases into two is what let a
 * dropped field silently strip every managed-only control from a managed
 * application while the card still looked correct.
 */
export function serviceManagementState(
  service: TechStackServiceCardSource,
): ServiceManagementState {
  const value = (service.management_state ?? "").trim().toLowerCase();
  if (value === "managed" || value === "observed") return value;
  return "unknown";
}

/**
 * Resolve the ownership dimension, refusing to continue when the source was
 * supposed to carry it. Use at boundaries fed by a canonical read model.
 */
export function requireServiceManagementState(
  service: TechStackServiceCardSource,
  context = "service-card",
): RegistryManagementState {
  const state = serviceManagementState(service);
  if (state === "unknown") {
    throw new ManagementStateContractError(context, service.management_state);
  }
  return state;
}

export function serviceCardName(service: TechStackServiceCardSource): string {
  return service.display_name || service.name || "Service";
}

export function serviceTypeLabel(service: TechStackServiceCardSource): string {
  return (service.type || "service").replace(/[-_]/g, " ");
}

export function serviceTargetLabel(
  service: TechStackServiceCardSource,
): string | undefined {
  return (
    service.target_server ||
    service.node_name ||
    service.server_name ||
    service.target_server_id ||
    service.node_id ||
    service.server_id
  );
}

export function serviceCardMeta(service: TechStackServiceCardSource): string {
  const parts = [serviceTypeLabel(service)];
  const target = serviceTargetLabel(service);
  if (target) parts.push(target);
  if (service.port) parts.push(`:${service.port}`);
  return parts.join(" · ");
}

export function serviceCardPlacement(
  service: TechStackServiceCardSource,
  hint?: string,
): ServicePlacement {
  if (
    service.placement === "cloud" ||
    service.placement === "local" ||
    service.placement === "serverless" ||
    service.placement === "unknown"
  ) {
    return service.placement;
  }
  const text = [
    service.type,
    service.target_server,
    service.node_name,
    service.server_name,
    service.provider,
    hint,
  ]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();

  if (text.includes("serverless")) return "serverless";
  if (
    text.includes("cloud") ||
    text.includes("managed") ||
    text.includes("lease") ||
    text.includes("runtime")
  ) {
    return "cloud";
  }
  // Provider names are a much safer fallback than a service/container name.
  // Hostinger is intentionally listed here: its VPS instances are Cloud
  // targets even though the services discovered on them are Docker-local.
  if (
    [
      "hostinger",
      "ionos",
      "centron",
      "hetzner",
      "digitalocean",
      "aws",
      "azure",
      "gcp",
      "google cloud",
      "vultr",
      "linode",
    ].some((provider) => text.includes(provider))
  ) {
    return "cloud";
  }
  if (
    text.includes("local") ||
    text.includes("device") ||
    text.includes("homelab")
  ) {
    return "local";
  }
  return "unknown";
}

/**
 * A migration badge must represent an actual operation, not a stale generic
 * status string.  Registry/operations callers may provide either of the two
 * persisted operation fields while older payloads simply render their normal
 * observed health.
 */
export function serviceHasActiveMigration(
  service: TechStackServiceCardSource,
): boolean {
  const operationState = (service.operation_state || "").trim().toLowerCase();
  if (
    [
      "queued",
      "running",
      "waiting",
      "deploying",
      "migrating",
      "pending_verification",
    ].includes(operationState)
  ) {
    return true;
  }
  // migration_status is a projection/status label, not an operation receipt.
  // It can survive an unavailable executor or a partial refresh, so it must
  // never create a migration animation by itself. The explicit operation
  // state above is populated only when a real job is being tracked.
  return false;
}

export function serviceCardStatus(
  service: TechStackServiceCardSource,
): ServiceStatusKind {
  const state = (service.status || "").toLowerCase();
  // Ownership and runtime health are independent dimensions.  An observed
  // service is neither frozen nor migrating merely because it is unmanaged.
  if (state === "archived") return "frozen";
  if (serviceHasActiveMigration(service)) return "migrating";
  if (state.includes("update")) return "update";
  if (
    state === "error" ||
    state === "failed" ||
    state === "degraded" ||
    state === "unhealthy"
  ) {
    return "error";
  }
  if (state === "stopped" || state === "offline") return "stopped";
  if (["starting", "pending", "adopting"].includes(state)) return "pending";
  if (state === "" || state === "unknown") return "unknown";
  return "running";
}

export function serviceCardStatusMessage(
  service: TechStackServiceCardSource,
): string | undefined {
  if (service.status === "archived") {
    return "Archived source service.";
  }
  if (
    service.status === "error" ||
    service.status === "failed" ||
    service.status === "unhealthy"
  ) {
    return service.move_blocked_reason || "Service reported an error.";
  }
  if (service.status === "starting") {
    return "Waiting for a Docker health or endpoint probe.";
  }
  if (service.status === "unknown") {
    return "No current runtime observation is available.";
  }
  if (service.status === "reachable") {
    return "Endpoint is reachable, but protected service health is not verified.";
  }
  if (service.status === "pending") {
    return "Waiting for placement.";
  }
  return undefined;
}

export function serviceCardMetrics(
  service: TechStackServiceCardSource,
): ServiceMetric[] {
  const metrics: ServiceMetric[] = [
    { label: "Type", value: serviceTypeLabel(service) },
  ];
  const target = serviceTargetLabel(service);
  if (target) metrics.push({ label: "Server", value: target });
  if (service.port)
    metrics.push({ label: "Port", value: String(service.port) });
  return metrics;
}

export function openServiceUrl(service: TechStackServiceCardSource): void {
  if (!service.url) return;
  window.open(service.url, "_blank", "noopener,noreferrer");
}
