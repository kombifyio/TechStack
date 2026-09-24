export type WizardJoinAdditionMode = "join" | "found";

export function legacyJoinWizardIdempotencyStorageKey(stackId: string): string {
  return `wizardJoinIdempotencyKey:${stackId}`;
}

export function joinWizardIdempotencyStorageKey(
  stackId: string,
  mode: WizardJoinAdditionMode = "join",
): string {
  return `${legacyJoinWizardIdempotencyStorageKey(stackId)}:${mode}`;
}

export function readJoinWizardIdempotencyKey(
  stackId: string,
  mode: WizardJoinAdditionMode = "join",
): string | null {
  const storageKey = joinWizardIdempotencyStorageKey(stackId, mode);
  const existing = sessionStorage.getItem(storageKey);
  if (existing?.trim()) return existing;
  const legacy = sessionStorage.getItem(
    legacyJoinWizardIdempotencyStorageKey(stackId),
  );
  if (!legacy?.trim()) return null;
  sessionStorage.setItem(storageKey, legacy);
  sessionStorage.removeItem(legacyJoinWizardIdempotencyStorageKey(stackId));
  return legacy;
}

export function mintJoinWizardIdempotencyKey(
  stackId: string,
  mode: WizardJoinAdditionMode = "join",
): string {
  const existing = readJoinWizardIdempotencyKey(stackId, mode);
  if (existing) return existing;
  const storageKey = joinWizardIdempotencyStorageKey(stackId, mode);
  const generated =
    typeof crypto?.randomUUID === "function"
      ? crypto.randomUUID()
      : `join-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  sessionStorage.setItem(storageKey, generated);
  return generated;
}

export function clearJoinWizardIdempotencyKeys(stackId: string): void {
  if (!stackId) return;
  sessionStorage.removeItem(legacyJoinWizardIdempotencyStorageKey(stackId));
  for (const mode of ["join", "found"] as const) {
    sessionStorage.removeItem(joinWizardIdempotencyStorageKey(stackId, mode));
  }
}
