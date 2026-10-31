/**
 * The creation processes as data: one flow and five steps, and each process
 * names what it cannot change and which run it ends in. A frame mounts a
 * process; it never decides which steps exist.
 */
import type { CreationStepKey } from "./steps";

export type CreationRunKind = "found" | "join" | "extend" | "none";

/** What the Creation Screen follows after the run was accepted. */
export type CreationOperation = "stack" | "add-server" | "add-use-case";

export type CreationLayoutFlow = "add-server" | "add-use-case";

export interface CreationProcess {
  key:
    | "stack"
    | "place-on-server"
    | "add-server"
    | "add-server-found"
    | "add-use-case"
    | "homelab-settings";
  /** Steps stay visible but cannot be opened; the value is the reason's message key. */
  inactive: Partial<Record<CreationStepKey, string>>;
  run: CreationRunKind;
  operation: CreationOperation | null;
  /** The run needs at least one selected use case. */
  requireGoal: boolean;
  /** Access and users come from the deployment the run joins. */
  inheritsAccess: boolean;
  /** The owner already exists; nothing about the sign-in is asked or sent. */
  reusesOwner: boolean;
  /** A first run adopts the defaults of its deployment lane (hosted, self-hosted). */
  appliesLaneDefaults: boolean;
  /** The Server step asks which role the server takes in the deployment. */
  asksServerRole: boolean;
  /** Asks for the recommended distribution when a deployment is known. */
  layoutFlow?: CreationLayoutFlow;
}

const INHERITED = "wizard.step.inherited";
const EXISTING_OWNER = "wizard.step.existingOwner";

const base = {
  inactive: {},
  operation: null,
  requireGoal: false,
  inheritsAccess: false,
  reusesOwner: false,
  appliesLaneDefaults: true,
  asksServerRole: false,
} satisfies Partial<CreationProcess>;

export const CREATION_PROCESSES: Record<
  CreationProcess["key"],
  CreationProcess
> = {
  /** A new deployment on a new server. */
  stack: {
    ...base,
    key: "stack",
    run: "found",
    operation: "stack",
  },
  /** A new deployment on a server that is already enrolled. */
  "place-on-server": {
    ...base,
    key: "place-on-server",
    run: "found",
    operation: "stack",
    inactive: {
      server: INHERITED,
      access: INHERITED,
      users: INHERITED,
      login: EXISTING_OWNER,
    },
    inheritsAccess: true,
    reusesOwner: true,
  },
  /** One more server for an existing deployment. */
  "add-server": {
    ...base,
    key: "add-server",
    run: "join",
    operation: "add-server",
    inactive: {
      access: INHERITED,
      users: INHERITED,
      login: EXISTING_OWNER,
    },
    inheritsAccess: true,
    reusesOwner: true,
    appliesLaneDefaults: false,
    asksServerRole: true,
    layoutFlow: "add-server",
  },
  /** A new deployment started from the add-server page, same owner. */
  "add-server-found": {
    ...base,
    key: "add-server-found",
    run: "found",
    operation: "stack",
    inactive: { login: EXISTING_OWNER },
    reusesOwner: true,
    appliesLaneDefaults: false,
    asksServerRole: true,
    layoutFlow: "add-server",
  },
  /** More use cases on the servers of an existing deployment. */
  "add-use-case": {
    ...base,
    key: "add-use-case",
    run: "extend",
    operation: "add-use-case",
    inactive: {
      server: INHERITED,
      access: INHERITED,
      users: INHERITED,
      login: EXISTING_OWNER,
    },
    requireGoal: true,
    inheritsAccess: true,
    reusesOwner: true,
    appliesLaneDefaults: false,
    layoutFlow: "add-use-case",
  },
  /** Users and sign-in of a running homelab, through its identity runtime. */
  "homelab-settings": {
    ...base,
    key: "homelab-settings",
    run: "none",
    inactive: {
      goals: "wizard.settings.goalsElsewhere",
      server: "wizard.settings.serverElsewhere",
      access: "wizard.settings.accessUnsupported",
    },
    inheritsAccess: true,
    reusesOwner: true,
    appliesLaneDefaults: false,
  },
};

/**
 * The same process with its Server step open: the person chose one of their
 * existing servers on that step and may still change the choice there.
 */
export function withServerChoice(process: CreationProcess): CreationProcess {
  if (!process.inactive.server) return process;
  const { server: _server, ...inactive } = process.inactive;
  return { ...process, inactive };
}
