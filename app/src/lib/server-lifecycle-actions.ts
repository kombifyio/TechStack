import type { StackKitLifecycleOperation } from "#lib/api/stacks.js";
import type { CanonicalServer } from "#lib/api/registry.js";

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
    label: "Plan changes",
    description: "Preview the next StackKit change for this server.",
    mutates: false,
  },
  apply: {
    operation: "apply",
    label: "Apply plan",
    description: "Apply the prepared StackKit plan to this server.",
    mutates: true,
  },
  verify: {
    operation: "verify",
    label: "Verify installation",
    description: "Verify release receipt, Owner binding, and runtime state.",
    mutates: false,
  },
  upgrade: {
    operation: "upgrade",
    label: "Upgrade to latest",
    description: "Upgrade through the published StackKits release channel.",
    mutates: true,
  },
  drift_detect: {
    operation: "drift_detect",
    label: "Detect drift",
    description: "Compare the running server with its desired StackKit state.",
    mutates: false,
  },
  drift_reconcile: {
    operation: "drift_reconcile",
    label: "Reconcile drift",
    description: "Restore the desired StackKit state on this server.",
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
