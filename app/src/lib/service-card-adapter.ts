import type {
  ServiceCardAction,
  ServicePlacement,
  ServiceStatusKind,
  ServiceUpdate,
} from "@kombiverselabs/ui/service";
import {
  ManagementStateContractError,
  type RegistryManagementState,
} from "#lib/api/registry.js";
import {
  parsePlacementClass,
  type ServiceCatalogRef,
  type ServiceVersionInfo,
} from "#lib/api/services.js";
import {
  brandDomainForTool,
  pickAppLogo,
  type LogoTheme,
} from "#lib/brand-logo.js";
import {
  appLogoKey,
  getAppLogoOverride,
} from "#lib/app-logo-overrides.svelte.js";
import { logoTheme } from "#lib/stores/logo-theme.svelte.js";

import { tr } from "#lib/i18n.svelte.js";
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
  /**
   * The backend's own placement answer (`local|cloud|managed|unknown`). It is
   * consulted first; `unknown` falls through to the heuristics below.
   */
  placement_class?: string;
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
  return service.display_name || service.name || tr("ui.services.service");
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
  const measured = parsePlacementClass(service.placement_class);
  if (measured && measured !== "unknown") return measured;
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
    return tr("ui.serviceCardAdapter.archivedSourceService");
  }
  if (
    service.status === "error" ||
    service.status === "failed" ||
    service.status === "unhealthy"
  ) {
    return (
      service.move_blocked_reason ||
      tr("ui.serviceCardAdapter.serviceReportedAnError")
    );
  }
  if (service.status === "starting") {
    return tr("ui.serviceCardAdapter.waitingForADockerHealth");
  }
  if (service.status === "unknown") {
    return tr("ui.serviceCardAdapter.noCurrentRuntimeObservationIs");
  }
  if (service.status === "reachable") {
    return tr("ui.serviceCardAdapter.endpointIsReachableButProtected");
  }
  if (service.status === "pending") {
    return tr("ui.serviceCardAdapter.waitingForPlacement");
  }
  if ((service.management_state ?? "").trim().toLowerCase() === "observed") {
    return tr("ui.serviceCardAdapter.observedOnlyAdoptToManage");
  }
  return undefined;
}

/** Managed-service actions the governed endpoint executes today. */
export type GovernedServiceAction =
  "start" | "stop" | "restart" | "logs" | "freeze" | "unfreeze" | "update";

const GOVERNED_ACTIONS: readonly GovernedServiceAction[] = [
  "logs",
  "start",
  "stop",
  "restart",
  "update",
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
  "update",
]);
/** Guardrail changes are owner decisions, so they carry the approval gate. */
const APPROVAL_ACTIONS: ReadonlySet<GovernedServiceAction> = new Set([
  "start",
  "stop",
  "restart",
  "update",
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
  /** Why the service is locked, e.g. "Locked by alex · 2h ago". */
  lockedReason?: string;
  /** The governed action currently in flight for this service, if any. */
  activeAction?: string;
  /** True while the post-mutation StackKits verify pass is running. */
  converging?: boolean;
  /** Runs a governed action (owner approval handled by the caller). */
  onAction: (action: GovernedServiceAction) => void;
  /**
   * "Stack it": adopt an observed service into a stack. Rendered only when the
   * backend granted `adopt` and the caller can open the adoption flow.
   */
  onAdopt?: () => void;
  /**
   * Why "Stack it" is withheld from an Unstacked service. When set and the
   * backend did not grant `adopt`, the card keeps the action, disabled with
   * this reason, so compact and expanded cards explain the denial instead of
   * silently dropping the offer.
   */
  adoptDeniedReason?: string;
  /**
   * An update is known for this service. A locked service then still shows
   * the Update action, disabled with the lock reason, so the strip never
   * offers something that silently vanished.
   */
  updateAvailable?: boolean;
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
        state === "disabled"
          ? tr("ui.serviceCardAdapter.anotherGovernedActionIsRunning")
          : undefined,
      onSelect: () => context.onAction(action),
    };
  });
  // A locked service has no mutating actions granted, so the buttons an owner
  // is used to would silently disappear. Render them disabled with the lock
  // reason instead: the API contract stays narrow, the surface stays honest.
  if (context.locked) {
    for (const action of MUTATING_ACTIONS) {
      if (granted.includes(action)) continue;
      if (action === "update" && !context.updateAvailable) continue;
      actions.push({
        id: action,
        state: "disabled",
        disabledReason:
          context.lockedReason ?? tr("ui.serviceCardAdapter.locked"),
        onSelect: () => {},
      });
    }
  }
  if (context.onAdopt && context.allowedActions.includes("adopt")) {
    actions.push({ id: "adopt", state: "ready", onSelect: context.onAdopt });
  } else if (context.adoptDeniedReason) {
    actions.push({
      id: "adopt",
      state: "disabled",
      disabledReason: context.adoptDeniedReason,
      onSelect: () => {},
    });
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
  return actor
    ? tr("ui.serviceCardAdapter.lockedBy", { actor })
    : tr("ui.serviceCardAdapter.locked");
}

/** The installed version chip, only when a real measurement exists. */
export function serviceCardVersion(
  version?: ServiceVersionInfo | null,
): string | undefined {
  return version?.current?.trim() || undefined;
}

/**
 * The update strip. It exists only for a measured update: the backend said one
 * is available AND a latest version is known. An unknown latest never produces
 * an invented target.
 */
export function serviceCardUpdate(
  version?: ServiceVersionInfo | null,
): ServiceUpdate | undefined {
  if (!version?.update_available) return undefined;
  const to = version.latest?.trim();
  if (!to) return undefined;
  return {
    from: version.current?.trim() || tr("ui.serviceCards.versionUnknown"),
    to,
  };
}

/** Vendor domain for the logo: the backend catalog first, then the name map. */
export function serviceCardLogoDomain(
  catalog: ServiceCatalogRef | null | undefined,
  ...names: Array<string | undefined | null>
): string {
  return (
    catalog?.brand_domain?.trim() || brandDomainForTool(catalog?.id, ...names)
  );
}

/**
 * The logo to draw, or undefined for the monogram or glyph: this viewer's
 * override of the application first, then the recognition's own logo (the
 * light or dark variant of the theme), then the mirrored icon of the vendor
 * domain. Every URL points at our own asset host. `theme` defaults to the
 * current UI theme; pass "light" for a plate that stays light in every theme.
 */
export function serviceCardLogoSrc(
  domain: string,
  source: {
    catalog?: ServiceCatalogRef | null;
    applicationKey?: string | null;
    theme?: LogoTheme;
  } = {},
): string | undefined {
  return pickAppLogo({
    override: getAppLogoOverride(appLogoKey(source.applicationKey)),
    logo: source.catalog?.logo,
    removed: source.catalog?.logo_removed,
    domain,
    theme: source.theme ?? logoTheme(),
  });
}

/**
 * Domain and logo of one application with this viewer's override applied. A
 * hidden logo also empties the domain, so the logo scope draws no mirrored
 * icon behind it either. `names` are the identity candidates of the domain
 * lookup, as for `serviceCardLogoDomain`.
 */
export function serviceCardLogo(
  catalog: ServiceCatalogRef | null | undefined,
  applicationKey: string | null | undefined,
  names: Array<string | undefined | null>,
  theme?: LogoTheme,
): { domain: string; src: string | undefined } {
  const override = getAppLogoOverride(appLogoKey(applicationKey));
  const hidden =
    override?.mode === "hidden" ||
    (catalog?.logo_removed === true && override?.mode !== "icon");
  const domain = hidden ? "" : serviceCardLogoDomain(catalog, ...names);
  return {
    domain,
    src: serviceCardLogoSrc(domain, { catalog, applicationKey, theme }),
  };
}

/**
 * Ports that conventionally speak plain HTTP. Mirrors the agent's
 * `webContainerPorts` (pkg/agent/httpguard/discovery_ports.go; keep both in
 * step). The agent emits an explicit scheme whenever it proves a web UI, so a
 * schemeless internal `host:port` is opened only when its port is one of these;
 * a DNS, MQTT, database or TLS port without a proven scheme gets no Open link.
 * The address carries the host port, the agent's list is keyed by container
 * port: an unusual mapping can still land on either side of this check.
 */
const CONVENTIONAL_HTTP_PORTS: ReadonlySet<number> = new Set([
  80, 3000, 3001, 3456, 4533, 5000, 5055, 6767, 7878, 8000, 8080, 8081, 8096,
  8123, 8181, 8384, 8686, 8787, 8888, 8989, 9000, 9090, 9696, 13378,
]);

/**
 * The address "Open" navigates to, or undefined. Only http(s) URLs are ever
 * opened. A schemeless internal `host:port` address is lifted to http only when
 * its port is a conventional HTTP port (see `CONVENTIONAL_HTTP_PORTS`); any
 * other schemeless address stays text, never a guessed, broken link.
 */
export function serviceOpenUrl(access?: {
  kind?: string;
  open_url?: string;
  address?: string;
}): string | undefined {
  if (!access) return undefined;
  const isHttp = (value: string | undefined): boolean => {
    if (!value) return false;
    try {
      const url = new URL(value);
      return url.protocol === "http:" || url.protocol === "https:";
    } catch {
      return false;
    }
  };
  if (access.open_url && isHttp(access.open_url)) return access.open_url;
  if (access.address && isHttp(access.address)) return access.address;
  const address = access.address?.trim();
  if (access.kind !== "internal" || !address) return undefined;
  const match = /^[A-Za-z0-9.-]+:(\d{1,5})(\/[^\s]*)?$/.exec(address);
  if (match && CONVENTIONAL_HTTP_PORTS.has(Number(match[1]))) {
    return `http://${address}`;
  }
  return undefined;
}
