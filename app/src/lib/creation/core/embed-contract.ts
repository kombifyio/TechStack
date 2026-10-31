/**
 * What a host page and the embedded creation module say to each other. Techstack
 * owns this contract; a host reads the event schema and ignores any other. JSON
 * schemas: docs/contracts/techstack.creation-*.v1.schema.json.
 */
import { CREATION_STEP_KEYS, type CreationStepKey } from "./steps";

export const CREATION_ENTRY_SCHEMA = "techstack.creation-entry/v1";
export const CREATION_EVENT_SCHEMA = "techstack.creation-event/v1";
export const CREATION_EMBED_PATH = "/embed/creation";

export const CREATION_ENTRY_PROCESSES = [
  "add-use-case",
  "place-on-server",
  "add-server",
  "stack",
] as const;
export type CreationEntryProcess = (typeof CREATION_ENTRY_PROCESSES)[number];

/** `sheet`: the whole process inside a dialog. `step`: one step alone. */
export type CreationEntryFrame = "sheet" | "step";

export interface CreationEntry {
  process: CreationEntryProcess;
  frame: CreationEntryFrame;
  step?: CreationStepKey;
  useCase?: string;
  /** Guided-setup answers as `<question>:<choice>,…`. */
  setup?: string;
  /** The deployment to add to. */
  stack?: string;
  /** The enrolled server the owner started from. */
  server?: string;
}

const ID = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/;
const SETUP = /^[a-z0-9:,-]{1,2048}$/;

const matching = (value: string | null, pattern: RegExp) =>
  value && pattern.test(value) ? value : undefined;

const stepOf = (value: unknown): CreationStepKey | undefined =>
  CREATION_STEP_KEYS.find((key) => key === value);

/** The entry a link names, or null for a module that only waits (warm). */
export function parseCreationEntry(params: {
  get(name: string): string | null;
}): CreationEntry | null {
  const process = CREATION_ENTRY_PROCESSES.find(
    (key) => key === params.get("process"),
  );
  if (!process) return null;
  return {
    process,
    frame: params.get("frame") === "step" ? "step" : "sheet",
    step: stepOf(params.get("step")),
    useCase: matching(params.get("use-case"), ID),
    setup: matching(params.get("setup"), SETUP),
    stack: matching(params.get("stack"), ID),
    server: matching(params.get("server"), ID),
  };
}

export function creationEntryPath(entry: CreationEntry): string {
  const params = new URLSearchParams({
    process: entry.process,
    frame: entry.frame,
  });
  if (entry.step) params.set("step", entry.step);
  if (entry.useCase) params.set("use-case", entry.useCase);
  if (entry.setup) params.set("setup", entry.setup);
  if (entry.stack) params.set("stack", entry.stack);
  if (entry.server) params.set("server", entry.server);
  return `${CREATION_EMBED_PATH}?${params.toString()}`;
}

/** The same entry on Techstack's own pages, for "continue in Techstack". */
export function creationFullPath(entry: CreationEntry): string {
  const params = new URLSearchParams();
  if (entry.useCase) params.set("use-case", entry.useCase);
  if (entry.setup) params.set("setup", entry.setup);
  if (entry.server) params.set("server", entry.server);
  const stack = entry.stack ? encodeURIComponent(entry.stack) : "";
  let path = "/use-cases/new";
  if (entry.process === "add-use-case" && stack)
    path = `/stacks/${stack}/use-cases/new`;
  if (entry.process === "add-server" && stack)
    path = `/stacks/${stack}/servers/new`;
  if (entry.process === "stack") {
    path = "/stacks/new";
    params.set("target", "new");
  }
  const query = params.toString();
  return query ? `${path}?${query}` : path;
}

/** Keeps one step of a process; the module asks nothing else. */
export function creationStepOnly<
  Process extends { inactive: Partial<Record<CreationStepKey, string>> },
>(process: Process, step: CreationStepKey, reasonKey: string): Process {
  const inactive = Object.fromEntries(
    CREATION_STEP_KEYS.filter((key) => key !== step).map((key) => [
      key,
      process.inactive[key] ?? reasonKey,
    ]),
  );
  return { ...process, inactive };
}

/** Where a person is, for `step-changed`: a creation step or the choice of target. */
export type CreationEventStep = CreationStepKey | "target";

export type CreationEvent =
  | { event: "ready" }
  | { event: "step-changed"; step: CreationEventStep; summary: string }
  | { event: "resize"; height: number }
  | { event: "submitted"; runId: string }
  | { event: "progress"; runId: string; phase: string }
  | {
      event: "completed";
      runId: string;
      stackId: string;
      outcome: "succeeded" | "failed" | "accepted";
    }
  | { event: "cancelled" }
  | { event: "open-full"; path: string };

export type CreationCommand =
  | { command: "go-to-step"; step: CreationStepKey }
  | { command: "close-request" };

export function parseCreationCommand(data: unknown): CreationCommand | null {
  if (!data || typeof data !== "object") return null;
  const message = data as Record<string, unknown>;
  if (message.schema !== CREATION_EVENT_SCHEMA) return null;
  if (message.command === "close-request") return { command: "close-request" };
  if (message.command !== "go-to-step") return null;
  const step = stepOf(message.step);
  return step ? { command: "go-to-step", step } : null;
}
