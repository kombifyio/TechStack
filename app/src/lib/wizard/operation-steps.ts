import type { WizardStep } from "./types";

/**
 * Navigation over the five Creation Wizard modules for one operation. An
 * operation names the modules it cannot change with a reason; those stay
 * visible in the stepper but are skipped, so a step is only ever editable
 * where a supported backend path exists.
 */
export interface OperationStepPlan {
  activeIds: number[];
  first: number;
  last: number;
  next(current: number): number | undefined;
  previous(current: number): number | undefined;
}

export function operationStepPlan(
  steps: WizardStep[],
  inactiveReasons: Record<number, string>,
): OperationStepPlan {
  const activeIds = steps
    .filter((step) => !inactiveReasons[step.id])
    .map((step) => step.id);
  return {
    activeIds,
    first: activeIds[0] ?? steps[0]?.id ?? 1,
    last: activeIds.at(-1) ?? steps.at(-1)?.id ?? 1,
    next: (current) => activeIds.find((id) => id > current),
    previous: (current) => activeIds.findLast((id) => id < current),
  };
}
