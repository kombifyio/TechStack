/**
 * Quick actions on an application card (owner direction 2026-10-10).
 *
 * A stacked application is managed, so its card offers more: Restart sits on
 * the card next to Back up now, Start, Stop and Update (when one is
 * available) in the "…" menu.
 * An unstacked application only offers "Stack it", disabled with the
 * backend's reason when adoption is withheld. Every action comes from
 * the backend's `allowed_actions` grant, so a card never shows an action the
 * control plane cannot run. Logs and Copy address are always in the menu.
 *
 * The provider lives in context so a page creates one tracker and every card
 * on it, Node cards included, renders the same executing truth.
 */
import { getContext, setContext, type Component } from "svelte";
import {
  Archive,
  CircleArrowUp,
  Copy,
  Layers,
  Play,
  RotateCw,
  ScrollText,
  Square,
} from "@lucide/svelte";
import type { CanonicalService } from "../api/services";
import { confirmInApp } from "../dialogs/in-app-dialog";
import { tr } from "../i18n.svelte";
import { serviceIsLocked } from "../service-card-adapter";
import { applicationOpenUrl, isStackedApp } from "../dashboard/homelab-model";
import { actionLabel, adoptionDeniedReason } from "./application-actions";
import {
  ServiceActionTracker,
  isActionActive,
} from "./service-action-tracker.svelte";

export interface AppCardAction {
  id: string;
  label: string;
  icon: Component;
  /** Shown on the card itself; otherwise it lives in the "…" menu. */
  direct?: boolean;
  /** A management action, styled so a stacked card reads as more capable. */
  managed?: boolean;
  busy?: boolean;
  /**
   * Set when the backend withholds the action: the card keeps it, disabled
   * with this reason, instead of silently dropping it.
   */
  disabledReason?: string;
  href?: string;
  onSelect?: () => void | Promise<void>;
}

type FanoutAction = "start" | "stop" | "restart" | "update" | "backup";

const CONTEXT_KEY = Symbol("app-quick-actions");

export interface AppQuickActions {
  actionsFor: (members: readonly CanonicalService[]) => AppCardAction[];
  /** The last failure, cleared on the next action. */
  readonly error: string | null;
}

export function servicesHref(serviceId: string, extra = ""): string {
  return `/services?service=${encodeURIComponent(serviceId)}${extra}`;
}

function actionName(action: FanoutAction): string {
  if (action === "update") return tr("ui.nodeRow.update");
  if (action === "backup") return tr("ui.nodeRow.backupNow");
  return actionLabel(action);
}

export function createAppQuickActions(options: {
  refresh: () => Promise<void>;
  services: () => readonly CanonicalService[];
}) {
  const tracker = new ServiceActionTracker({
    refresh: options.refresh,
    observedRevision: (serviceId) =>
      options.services().find((service) => service.id === serviceId)
        ?.inventory_revision,
  });
  let error = $state<string | null>(null);

  function granting(
    members: readonly CanonicalService[],
    action: string,
  ): CanonicalService[] {
    return members.filter(
      (member) =>
        member.allowed_actions.includes(action) &&
        !serviceIsLocked(member.mutation_lock),
    );
  }

  async function run(
    name: string,
    action: FanoutAction,
    targets: CanonicalService[],
  ) {
    error = null;
    const label = actionName(action);
    const approved = await confirmInApp({
      title: tr("ui.services.actionTitle", { action: label, name }),
      message:
        targets.length > 1
          ? tr("ui.serviceCards.confirmFanout", {
              action: label.toLowerCase(),
              name,
              count: targets.length,
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
    if (!approved) return;
    const failures = await Promise.all(
      targets.map((target) => tracker.run(target, action)),
    );
    error = failures.find((failure) => failure !== null) ?? null;
  }

  function actionsFor(members: readonly CanonicalService[]): AppCardAction[] {
    const primary = members[0];
    if (!primary) return [];
    const name = primary.application_display_name || primary.name;
    const running = members
      .map((member) => tracker.jobs[member.id])
      .find((job) => isActionActive(job));
    const actions: AppCardAction[] = [];

    if (isStackedApp(members)) {
      const fanout = (
        action: FanoutAction,
        icon: Component,
        direct: boolean,
      ) => {
        let targets =
          action === "update"
            ? granting(members, "update").filter(
                (member) => member.version?.update_available,
              )
            : granting(members, action);
        // A backup covers the whole stack on its node, so one component is
        // enough to start it.
        if (action === "backup") targets = targets.slice(0, 1);
        if (targets.length === 0) return;
        actions.push({
          id: action,
          label: actionName(action),
          icon,
          direct,
          managed: true,
          busy: running?.action === action,
          onSelect: () => run(name, action, targets),
        });
      };
      fanout("restart", RotateCw, true);
      fanout("backup", Archive, true);
      fanout("update", CircleArrowUp, false);
      fanout("start", Play, false);
      fanout("stop", Square, false);
    } else {
      const adoptable = granting(members, "adopt")[0];
      const unstacked = members.find(
        (member) => member.management_state === "observed",
      );
      const deniedReason =
        !adoptable && unstacked ? adoptionDeniedReason(unstacked) : undefined;
      if (adoptable) {
        // Opens the source-bound adoption plan on the services page.
        actions.push({
          id: "adopt",
          label: tr("ui.nodeRow.stackIt"),
          icon: Layers,
          direct: true,
          href: servicesHref(adoptable.id, "&adopt=1"),
        });
      } else if (deniedReason) {
        actions.push({
          id: "adopt",
          label: tr("ui.nodeRow.stackIt"),
          icon: Layers,
          direct: true,
          disabledReason: deniedReason,
        });
      }
    }

    actions.push({
      id: "logs",
      label: tr("ui.nodeRow.appLogs", { app: name }),
      icon: ScrollText,
      href: servicesHref(primary.id, "&sheet=logs"),
    });
    const url = applicationOpenUrl([...members]);
    if (url) {
      actions.push({
        id: "copy",
        label: tr("ui.nodeRow.copyAddress", { app: name }),
        icon: Copy,
        onSelect: async () => {
          try {
            await navigator.clipboard.writeText(url);
          } catch {
            error = tr("ui.nodeRow.copyFailed");
          }
        },
      });
    }
    return actions;
  }

  /** Starts the job loop; returns the cleanup for onMount. */
  function start(): () => void {
    tracker.start();
    const interval = setInterval(() => {
      if (tracker.hasActiveJobs() || tracker.hasConverging()) {
        void tracker.tick();
      }
    }, 3000);
    return () => {
      clearInterval(interval);
      tracker.stop();
    };
  }

  const provider: AppQuickActions = {
    actionsFor,
    get error() {
      return error;
    },
  };
  setContext(CONTEXT_KEY, provider);
  return { provider, start };
}

/** The page's quick-action provider, or undefined where none is set. */
export function appQuickActions(): AppQuickActions | undefined {
  return getContext<AppQuickActions | undefined>(CONTEXT_KEY);
}
