import type { StackKitLifecycleOperation } from "#lib/api/stacks.js";
import type { CanonicalServer } from "#lib/api/registry.js";

import { tr } from "#lib/i18n.svelte.js";
export interface ServerLifecycleAction {
  operation: StackKitLifecycleOperation;
  label: string;
  description: string;
  mutates: boolean;
}

/**
 * Presentation only. Which of these a server currently admits is the backend's
 * answer (`CanonicalServer.stack_actions`), derived there from lifecycle,
 * connection, agent binding and the observed kit — this module no longer
 * guesses it from a status string.
 */
const actions: Record<StackKitLifecycleOperation, ServerLifecycleAction> = {
  plan: {
    operation: "plan",
    get label() {
      return tr("ui.serverLifecycleActions.planChanges");
    },
    get description() {
      return tr("ui.serverLifecycleActions.previewTheNextStackKitChange");
    },
    mutates: false,
  },
  apply: {
    operation: "apply",
    get label() {
      return tr("ui.serverLifecycleActions.applyPlan");
    },
    get description() {
      return tr("ui.serverLifecycleActions.applyThePreparedStackKitPlan");
    },
    mutates: true,
  },
  verify: {
    operation: "verify",
    get label() {
      return tr("ui.serverLifecycleActions.verifyInstallation");
    },
    get description() {
      return tr("ui.serverLifecycleActions.verifyReleaseReceiptOwnerBinding");
    },
    mutates: false,
  },
  upgrade: {
    operation: "upgrade",
    get label() {
      return tr("ui.serverLifecycleActions.upgradeToLatest");
    },
    get description() {
      return tr(
        "ui.serverLifecycleActions.upgradeThroughThePublishedStackKits",
      );
    },
    mutates: true,
  },
  drift_detect: {
    operation: "drift_detect",
    get label() {
      return tr("ui.serverLifecycleActions.detectDrift");
    },
    get description() {
      return tr("ui.serverLifecycleActions.compareTheRunningServerWith");
    },
    mutates: false,
  },
  drift_reconcile: {
    operation: "drift_reconcile",
    get label() {
      return tr("ui.serverLifecycleActions.reconcileDrift");
    },
    get description() {
      return tr("ui.serverLifecycleActions.restoreTheDesiredStackKitState");
    },
    mutates: true,
  },
};

/**
 * The StackKit operations this server currently admits, in the backend's own
 * order. A grant this build has no presentation for is dropped rather than
 * rendered as a nameless button, and a server whose read model predates the
 * capability field offers nothing — absent is not "everything".
 */
export function serverLifecycleActions(
  server: CanonicalServer | undefined,
): ServerLifecycleAction[] {
  return (server?.stack_actions ?? [])
    .map((operation) => actions[operation as StackKitLifecycleOperation])
    .filter((action): action is ServerLifecycleAction => Boolean(action));
}
