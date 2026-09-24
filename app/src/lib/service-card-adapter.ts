import type {
  ServiceCardAction,
  ServicePlacement,
  ServiceStatusKind,
} from "@kombiverselabs/ui/service";
import {
  ManagementStateContractError,
  type RegistryManagementState,
} from "#lib/api/registry.js";

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
    service.placement === "managed" ||
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
  if ((service.management_state ?? "").trim().toLowerCase() === "observed") {
    return "Observed only — adopt to manage lifecycle.";
  }
  return undefined;
}

/** Managed-service actions the governed endpoint executes today. */
export type GovernedServiceAction =
  | "start"
  | "stop"
  | "restart"
  | "logs"
  | "freeze"
  | "unfreeze";

const GOVERNED_ACTIONS: readonly GovernedServiceAction[] = [
  "logs",
  "start",
  "stop",
  "restart",
  "freeze",
  "unfreeze",
];
/**
 * Actions the owner guardrail suppresses. `freeze`/`unfreeze` are excluded on
 * purpose: they change the guardrail rather than the runtime, and the backend
 * never offers both at once.
 */
const MUTATING_ACTIONS: ReadonlySet<GovernedServiceAction> = new Set([
  "start",
  "stop",
  "restart",
]);
/** Guardrail changes are owner decisions, so they carry the approval gate. */
const APPROVAL_ACTIONS: ReadonlySet<GovernedServiceAction> = new Set([
  "start",
  "stop",
  "restart",
  "freeze",
  "unfreeze",
]);

export interface ServiceCardActionContext {
  /** The backend capability grant (`ServiceRuntime.allowed_actions`). */
  allowedActions: readonly string[];
  /**
   * Resolved ownership dimension. Kept for callers that still gate on it; the
   * lock slot now comes from `allowedActions`, which is the backend's own
   * answer to whether this service can be locked.
   */
  managementState: ServiceManagementState;
  /**
   * The persisted owner guardrail (`mutation_lock`). While locked the backend
   * drops the mutating actions from `allowed_actions`; the card still renders
   * them, disabled with this reason, so a familiar button never just vanishes.
   */
  locked?: boolean;
  /** Why the service is locked, e.g. "Locked by marcel · 2h ago". */
  lockedReason?: string;
  /** The governed action currently in flight for this service, if any. */
  activeAction?: string;
  /** True while the post-mutation StackKits verify pass is running. */
  converging?: boolean;
  /** Runs a governed action (owner approval handled by the caller). */
  onAction: (action: GovernedServiceAction) => void;
  /** Opens the service UI; omitted when no safe address exists. */
  onOpen?: () => void;
  /** Opens the service detail sheet. */
  onDetails?: () => void;
}

/**
 * Build the capability-driven card action array: a button exists only for a
 * backend-granted action, mutations announce the owner-approval gate, and an
 * in-flight job renders on its own button (executing → verifying) while the
 * sibling mutations lock with an honest reason.
 */
export function serviceCardActions(
  context: ServiceCardActionContext,
): ServiceCardAction[] {
  const granted = GOVERNED_ACTIONS.filter((action) =>
    context.allowedActions.includes(action),
  );
  const busy = Boolean(context.activeAction);
  const actions: ServiceCardAction[] = granted.map((action) => {
    const isActive = context.activeAction === action;
    const state = isActive
      ? context.converging
        ? "verifying"
        : "executing"
      : busy && MUTATING_ACTIONS.has(action)
        ? "disabled"
        : APPROVAL_ACTIONS.has(action)
          ? "needs_approval"
          : "ready";
    return {
      id: action,
      state,
      disabledReason:
        state === "disabled" ? "Another governed action is running" : undefined,
      onSelect: () => context.onAction(action),
    };
  });
  // A locked service has no mutating actions granted, so the buttons an owner
  // is used to would silently disappear. Render them disabled with the lock
  // reason instead: the API contract stays narrow, the surface stays honest.
  if (context.locked) {
    for (const action of MUTATING_ACTIONS) {
      if (granted.includes(action)) continue;
      actions.push({
        id: action,
        state: "disabled",
        disabledReason: context.lockedReason ?? "Locked",
        onSelect: () => {},
      });
    }
  }
  if (context.onDetails) {
    actions.push({ id: "details", onSelect: context.onDetails });
  }
  if (context.onOpen) {
    actions.push({ id: "open", onSelect: context.onOpen });
  }
  return actions;
}

export function openServiceUrl(service: TechStackServiceCardSource): void {
  if (!service.url) return;
  try {
    const url = new URL(service.url);
    if (url.protocol !== "http:" && url.protocol !== "https:") return;
    window.open(service.url, "_blank", "noopener,noreferrer");
  } catch {
    // An invalid or relative target is display-only; never navigate it.
  }
}

/**
 * Whether the owner guardrail is engaged. An absent lock is unlocked: a
 * backend that predates the field has no guardrail, and reading its silence as
 * "locked" would freeze every service on an older deployment.
 */
export function serviceIsLocked(lock?: { state?: string }): boolean {
  return (lock?.state ?? "unlocked").trim().toLowerCase() === "locked";
}

/**
 * Human explanation of a lock, for the disabled-action tooltip and the detail
 * sheet chip. Falls back to the bare fact rather than inventing an actor.
 */
export function serviceLockReason(lock?: {
  state?: string;
  actor?: string;
  changed_at?: string;
}): string | undefined {
  if (!serviceIsLocked(lock)) return undefined;
  const actor = lock?.actor?.trim();
  return actor ? `Locked by ${actor}` : "Locked";
}
