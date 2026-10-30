/**
 * What each step reads and writes of the answers. The answers stay one
 * StackConfig (the draft and the run request are built from it); a slice is the
 * typed view a step gets, so the compiler refuses a step that reaches further.
 */
import type { OwnerConfig, StackConfig } from "#lib/wizard/types.js";

export type GoalsSlice = Pick<
  StackConfig,
  | "goals"
  | "useCaseSettings"
  | "useCaseCapabilities"
  | "useCaseSetupAnswers"
  | "advanced"
>;

/** Access reads the server mode (a managed server has no home access) and the goals. */
export type AccessView = Pick<StackConfig, "network" | "goals"> & {
  serverProvisioning: Pick<StackConfig["serverProvisioning"], "mode">;
};

/** Users shows the owner as the first person; the Login step writes that. */
export type UsersView = Pick<StackConfig, "audience" | "network"> & {
  owner: Pick<OwnerConfig, "displayName" | "email">;
};

export type OwnerSlice = Pick<StackConfig, "owner">;
