import { CANONICAL_USE_CASE_GOALS } from "./standardBundle";
import { createDefaultConfig, type StackConfig } from "./types";

/**
 * The starting answers of a surface that adds to an existing homelab (a new
 * server, or more use cases): no owner bootstrap and an empty use case
 * selection.
 *
 * The first-run defaults carry a pre-selected use case (vault). On an
 * expansion nobody made that selection, and the backend merges an expansion's
 * goals into the homelab backlog, so a default riding along would silently add
 * a use case the operator did not ask for. Start empty; what is sent is what
 * was picked.
 */
export function createExpansionConfig(preselected: readonly string[] = []): StackConfig {
  const next = createDefaultConfig();
  if (next.goals) {
    for (const goal of CANONICAL_USE_CASE_GOALS) {
      // A link may name use cases to start with (for example a Day-2 card);
      // only canonical ids count, and nothing is accepted until the owner
      // finishes the flow.
      next.goals[goal] = preselected.includes(goal);
    }
    next.goals.everything = false;
  }
  next.serverProvisioning.nodeRole = "worker";
  next.owner.bootstrapMode = "none";
  next.auth.requirePassword = false;
  next.auth.requireMfa = false;
  next.auth.allowPasswordless = false;
  return next;
}
