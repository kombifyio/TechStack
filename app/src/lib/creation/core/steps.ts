/** The five creation steps, in order. A step's number is its place here. */
export const CREATION_STEP_KEYS = [
  "goals",
  "server",
  "access",
  "users",
  "login",
] as const;

export type CreationStepKey = (typeof CREATION_STEP_KEYS)[number];

export function creationStepNumber(key: CreationStepKey): number {
  return CREATION_STEP_KEYS.indexOf(key) + 1;
}

export function creationStepKey(
  value: number | string | null | undefined,
): CreationStepKey | undefined {
  if (typeof value === "number") return CREATION_STEP_KEYS[value - 1];
  return CREATION_STEP_KEYS.find((key) => key === value);
}
