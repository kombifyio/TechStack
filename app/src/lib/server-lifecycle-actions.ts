import type {
  StackKitLifecycleOperation,
  StackOperationServer,
} from "$lib/api/stacks";

export interface ServerLifecycleAction {
  operation: StackKitLifecycleOperation;
  label: string;
  description: string;
  mutates: boolean;
}

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

const applyStates = new Set(["configured", "planned", "ready_to_apply"]);
const operationalStates = new Set([
  "active",
  "deployed",
  "healthy",
  "observed",
  "ready",
  "running",
]);
const driftStates = new Set(["degraded", "drift_detected", "drifted"]);

function normalized(value: string | undefined): string {
  return value?.trim().toLowerCase() ?? "";
}

export function canRunServerLifecycleActions(
  server: StackOperationServer,
): boolean {
  const connected = ["connected", "healthy", "running"].includes(
    normalized(server.status || server.health?.state),
  );
  const assigned =
    server.assignment === "stack" || Boolean(server.techstack_id?.trim());
  return Boolean(
    server.agent_id?.trim() && server.approved && assigned && connected,
  );
}

/**
 * The current UI contract for contextual lifecycle actions. Unknown states are
 * intentionally read-only: the backend can add an explicit capability model
 * later without the dashboard guessing that a mutation is safe.
 */
export function serverLifecycleActions(
  server: StackOperationServer,
): ServerLifecycleAction[] {
  if (!canRunServerLifecycleActions(server)) return [];

  const state = normalized(server.stackkit?.state);
  const result = [actions.plan];

  if (state) result.push(actions.verify);
  if (applyStates.has(state)) result.push(actions.apply);
  if (operationalStates.has(state)) {
    result.push(actions.upgrade, actions.drift_detect);
  }
  if (driftStates.has(state)) {
    result.push(actions.drift_detect, actions.drift_reconcile);
  }

  return result;
}
