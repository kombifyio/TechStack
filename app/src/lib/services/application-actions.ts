/**
 * Governed application actions: one owner approval, then the action runs on
 * every component that grants it. Shared by the Services board and the
 * application detail page so both gate and report identically.
 */
import type { ServiceCardAction } from "@kombiverselabs/ui/service";
import type { CanonicalService, ServiceApplication } from "../api/services";
import { confirmInApp } from "../dialogs/in-app-dialog";
import { tr } from "../i18n.svelte";
import {
  serviceCardActions,
  serviceIsLocked,
  serviceLockReason,
  serviceOpenUrl,
  type GovernedServiceAction,
} from "../service-card-adapter";
import {
  FANOUT_ACTIONS,
  adoptTarget,
  applicationLocked,
  applicationVersion,
  componentsGranting,
  logsTarget,
  updateTarget,
} from "./application-card";
import {
  isActionActive,
  type ServiceActionTracker,
} from "./service-action-tracker.svelte";

export type ApplicationAction = (typeof FANOUT_ACTIONS)[number] | "update";

/** Components an application-level action would run on right now. */
export function applicationActionTargets(
  application: Pick<ServiceApplication, "components" | "version">,
  action: ApplicationAction,
): CanonicalService[] {
  if (action === "update") {
    const target = updateTarget(application);
    return target ? [target] : [];
  }
  return componentsGranting(application, action);
}

export function actionLabel(action: string): string {
  const key = `ui.services.action.${action}`;
  const label = tr(key);
  return label === key ? action : label;
}

async function confirmApplicationAction(
  application: Pick<
    ServiceApplication,
    "display_name" | "components" | "version"
  >,
  action: ApplicationAction,
  targetCount: number,
): Promise<boolean> {
  const name = application.display_name;
  const label = actionLabel(action);
  if (action === "update") {
    const version = applicationVersion(application);
    return confirmInApp({
      title: tr("ui.services.actionTitle", { action: label, name }),
      message: tr("ui.serviceCards.confirmUpdate", {
        name,
        from: version?.current || tr("ui.serviceCards.versionUnknown"),
        to: version?.latest || tr("ui.serviceCards.versionUnknown"),
      }),
      confirmText: tr("ui.serviceCards.approveUpdate"),
      tone: "primary",
    });
  }
  return confirmInApp({
    title: tr("ui.services.actionTitle", { action: label, name }),
    message:
      targetCount > 1
        ? tr("ui.serviceCards.confirmFanout", {
            action: label.toLowerCase(),
            name,
            count: targetCount,
          })
        : tr("ui.services.confirmGoverned", {
            action: label.toLowerCase(),
            name,
          }),
    confirmText: tr("ui.services.approveAction", {
      action: label.toLowerCase(),
    }),
    tone: action === "stop" ? "warning" : "primary",
  });
}

/**
 * Run an application-level action behind one approval. Returns a failure
 * message, or null when nothing failed (including a declined approval).
 */
export async function runApplicationAction(
  tracker: ServiceActionTracker,
  application: ServiceApplication,
  action: ApplicationAction,
): Promise<string | null> {
  const targets = applicationActionTargets(application, action);
  if (targets.length === 0) return null;
  if (!(await confirmApplicationAction(application, action, targets.length))) {
    return null;
  }
  const failures = await Promise.all(
    targets.map((target) => tracker.run(target, action)),
  );
  return failures.find((failure) => failure !== null) ?? null;
}

/** Owner decision about the guardrail of one component; returns whether it was approved. */
export async function confirmLockChange(
  component: Pick<CanonicalService, "name">,
  action: "freeze" | "unfreeze",
): Promise<boolean> {
  const label = actionLabel(action);
  return confirmInApp({
    title: tr("ui.services.actionTitle", {
      action: label,
      name: component.name,
    }),
    message:
      action === "freeze"
        ? tr("ui.services.confirmLock", { name: component.name })
        : tr("ui.services.confirmUnlock", { name: component.name }),
    confirmText: tr("ui.services.approveAction", {
      action: label.toLowerCase(),
    }),
    tone: action === "freeze" ? "warning" : "primary",
  });
}

/**
 * Run one governed action on a single component behind its own owner approval
 * (guardrail changes get their own wording). Logs need no approval. Returns a
 * failure message, or null when nothing failed (including a declined approval).
 */
export async function runComponentAction(
  tracker: ServiceActionTracker,
  component: CanonicalService,
  action: GovernedServiceAction,
): Promise<string | null> {
  if (action === "freeze" || action === "unfreeze") {
    if (!(await confirmLockChange(component, action))) return null;
    return tracker.run(component, action);
  }
  if (action !== "logs") {
    const label = actionLabel(action);
    const approved = await confirmInApp({
      title: tr("ui.services.actionTitle", {
        action: label,
        name: component.name,
      }),
      message: tr("ui.services.confirmGoverned", {
        action: label.toLowerCase(),
        name: component.name,
      }),
      confirmText: tr("ui.services.approveAction", {
        action: label.toLowerCase(),
      }),
      tone: action === "stop" ? "warning" : "primary",
    });
    if (!approved) return null;
  }
  return tracker.run(component, action);
}

/**
 * The backend's explanation for a withheld adoption, by reason code. A source
 * without an exact container identity cannot be bound; otherwise the connected
 * Guard lacks a capable published StackKits release (or the backend predates
 * the assessment), which the general message covers.
 */
export function adoptionUnavailableReason(
  adoption?: CanonicalService["adoption"],
): string {
  switch (adoption?.reason_code) {
    case "application_source_identity_unavailable":
      return tr("ui.services.adoptionSourceIdentityUnavailable");
    case "application_adoption_unavailable":
      return tr("ui.services.adoptionReleaseUnavailable");
    default:
      return tr("ui.services.adoptionUnavailable");
  }
}

/**
 * Why "Stack it" is withheld from an Unstacked (observed) component, or
 * undefined when it is granted or does not apply. The owner lock wins: the
 * backend withholds `adopt` from a locked service even when the source could
 * be adopted.
 */
export function adoptionDeniedReason(
  component: Pick<
    CanonicalService,
    "management_state" | "allowed_actions" | "mutation_lock" | "adoption"
  >,
): string | undefined {
  if (component.management_state !== "observed") return undefined;
  if (component.allowed_actions.includes("adopt")) return undefined;
  if (serviceIsLocked(component.mutation_lock)) {
    return serviceLockReason(component.mutation_lock);
  }
  return adoptionUnavailableReason(component.adoption);
}

export interface ApplicationCardHandlers {
  /** Open the logs of one component (sheet or tab). */
  onLogs: (target: CanonicalService) => void;
  /** Run an approved application-level action. */
  onRun: (action: ApplicationAction) => void;
  /** Open the adoption flow for the observed component. */
  onAdopt: (target: CanonicalService) => void;
  /** Open the application URL. */
  onOpen: (url: string) => void;
  /** Open the application detail page; omitted on the page itself. */
  onDetails?: () => void;
}

/**
 * The capability-driven action array of an application card. Each action is
 * offered only when a component can execute it: start/stop/restart fan out to
 * every component that grants them behind one approval, update runs on the
 * primary component, Stack it adopts the observed component, logs open on the
 * primary component. In-flight jobs of any component render on their button.
 */
export function applicationCardActions(
  application: ServiceApplication,
  tracker: ServiceActionTracker,
  handlers: ApplicationCardHandlers,
): ServiceCardAction[] {
  const granted = new Set<string>();
  for (const action of ["logs", "start", "stop", "restart"] as const) {
    if (componentsGranting(application, action).length > 0) {
      granted.add(action);
    }
  }
  if (updateTarget(application)) granted.add("update");
  const adoptable = adoptTarget(application);
  if (adoptable) granted.add("adopt");
  const unstacked = application.components.find(
    (component) => component.management_state === "observed",
  );
  const running = application.components
    .map((component) => tracker.jobs[component.id])
    .find((job) => isActionActive(job));
  const lockedComponent = application.components.find((component) =>
    serviceIsLocked(component.mutation_lock),
  );
  const openUrl = serviceOpenUrl(application.access);
  return serviceCardActions({
    allowedActions: [...granted],
    managementState: "unknown",
    locked: applicationLocked(application),
    lockedReason: serviceLockReason(lockedComponent?.mutation_lock),
    activeAction: running?.action,
    converging: running?.converging,
    updateAvailable: Boolean(applicationVersion(application)?.update_available),
    onAction: (action: GovernedServiceAction) => {
      if (action === "logs") {
        const target = logsTarget(application);
        if (target) handlers.onLogs(target);
        return;
      }
      if (
        action === "start" ||
        action === "stop" ||
        action === "restart" ||
        action === "update"
      ) {
        handlers.onRun(action);
      }
    },
    onAdopt: adoptable ? () => handlers.onAdopt(adoptable) : undefined,
    adoptDeniedReason:
      !adoptable && unstacked ? adoptionDeniedReason(unstacked) : undefined,
    onOpen: openUrl ? () => handlers.onOpen(openUrl) : undefined,
    onDetails: handlers.onDetails,
  });
}

export interface ComponentCardHandlers {
  onAction: (action: GovernedServiceAction) => void;
  onAdopt: () => void;
  onOpen?: () => void;
  onDetails?: () => void;
}

/** The capability-driven action array of one component (one runtime row). */
export function componentCardActions(
  component: CanonicalService,
  tracker: ServiceActionTracker,
  handlers: ComponentCardHandlers,
): ServiceCardAction[] {
  const job = tracker.jobs[component.id];
  const active = isActionActive(job);
  return serviceCardActions({
    allowedActions: component.allowed_actions,
    managementState: component.management_state,
    locked: serviceIsLocked(component.mutation_lock),
    lockedReason: serviceLockReason(component.mutation_lock),
    activeAction: active ? job?.action : undefined,
    converging: job?.converging,
    updateAvailable: Boolean(component.version?.update_available),
    onAction: handlers.onAction,
    onAdopt: handlers.onAdopt,
    adoptDeniedReason: adoptionDeniedReason(component),
    onOpen: handlers.onOpen,
    onDetails: handlers.onDetails,
  });
}
