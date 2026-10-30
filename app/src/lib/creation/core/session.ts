/**
 * Navigation through one creation process, without a framework: the current
 * step, how far the person got, and the moves between steps. It follows the
 * store contract, so a frame reads it like any other store. What a move means
 * on screen (address, focus, the list of what is missing) belongs to the frame.
 */
import type { CreationProcess } from "./processes";
import {
  CREATION_STEP_KEYS,
  creationStepNumber as position,
  type CreationStepKey,
} from "./steps";

export interface CreationSessionState {
  process: CreationProcess;
  current: CreationStepKey;
  furthest: CreationStepKey;
}

/**
 * `moved`: the step is now current. `blocked`: the move stopped on this step,
 * which is not complete, and it is now current. `stay`: nothing to do.
 */
export type CreationMove =
  { kind: "moved" | "blocked"; step: CreationStepKey } | { kind: "stay" };

export interface CreationSession {
  subscribe(run: (state: CreationSessionState) => void): () => void;
  /** Steps of this process that can be opened, in order. */
  activeSteps(): CreationStepKey[];
  /** The active steps already reached, without the current one. */
  reachableSteps(): CreationStepKey[];
  lastStep(): CreationStepKey;
  /** The first active step before `target` that is not complete. */
  firstIncompleteBefore(target: CreationStepKey): CreationStepKey | undefined;
  next(): CreationMove;
  prev(): CreationMove;
  /** A reached step. Forward, every step on the way must still be complete. */
  goTo(target: CreationStepKey): CreationMove;
  /** Opens a step directly, for a move that needs no check. */
  open(target: CreationStepKey): void;
  setProcess(process: CreationProcess): void;
}

const activeOf = (process: CreationProcess) =>
  CREATION_STEP_KEYS.filter((key) => !process.inactive[key]);

export function createCreationSession(init: {
  process: CreationProcess;
  current?: CreationStepKey;
  furthest?: CreationStepKey;
  /** Asked at the moment of a move, so it may read live answers. */
  isComplete: (key: CreationStepKey) => boolean;
}): CreationSession {
  const first = activeOf(init.process)[0] ?? "goals";
  const start = init.current ?? first;
  let state: CreationSessionState = {
    process: init.process,
    current: start,
    furthest:
      init.furthest && position(init.furthest) > position(start)
        ? init.furthest
        : start,
  };
  const listeners = new Set<(state: CreationSessionState) => void>();

  function commit(next: Partial<CreationSessionState>) {
    state = { ...state, ...next };
    for (const listener of listeners) listener(state);
  }

  function open(target: CreationStepKey) {
    commit({
      current: target,
      furthest:
        position(target) > position(state.furthest) ? target : state.furthest,
    });
  }

  const activeSteps = () => activeOf(state.process);

  return {
    subscribe(run) {
      listeners.add(run);
      run(state);
      return () => listeners.delete(run);
    },
    activeSteps,
    reachableSteps: () =>
      activeSteps().filter(
        (key) =>
          position(key) <= position(state.furthest) && key !== state.current,
      ),
    lastStep: () => activeSteps().at(-1) ?? first,
    firstIncompleteBefore: (target) =>
      activeSteps().find(
        (key) => position(key) < position(target) && !init.isComplete(key),
      ),
    next() {
      const next = activeSteps().find(
        (key) => position(key) > position(state.current),
      );
      if (!next) return { kind: "stay" };
      if (!init.isComplete(state.current))
        return { kind: "blocked", step: state.current };
      open(next);
      return { kind: "moved", step: next };
    },
    prev() {
      const previous = activeSteps().findLast(
        (key) => position(key) < position(state.current),
      );
      if (!previous) return { kind: "stay" };
      open(previous);
      return { kind: "moved", step: previous };
    },
    goTo(target) {
      const reachable =
        target !== state.current &&
        activeSteps().includes(target) &&
        position(target) <= position(state.furthest);
      if (!reachable) return { kind: "stay" };
      if (position(target) > position(state.current)) {
        const blocked = activeSteps().find(
          (key) =>
            position(key) >= position(state.current) &&
            position(key) < position(target) &&
            !init.isComplete(key),
        );
        if (blocked) {
          open(blocked);
          return { kind: "blocked", step: blocked };
        }
      }
      open(target);
      return { kind: "moved", step: target };
    },
    open,
    setProcess(process) {
      if (process.key !== state.process.key) commit({ process });
    },
  };
}
