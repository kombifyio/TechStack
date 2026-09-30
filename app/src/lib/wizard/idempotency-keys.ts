export type WizardJoinAdditionMode = "join" | "found";

const JOIN_WIZARD_IDEMPOTENCY_STORAGE_PREFIX = "wizardJoinIdempotencyKey:";

export function legacyJoinWizardIdempotencyStorageKey(stackId: string): string {
  return `${JOIN_WIZARD_IDEMPOTENCY_STORAGE_PREFIX}${stackId}`;
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

function stackLifecycleRetryScope(
  stackId: string,
  jobId: string,
  kind: "deploy" | "provision",
): string {
  return `lifecycle-retry:${encodeURIComponent(stackId)}:${encodeURIComponent(jobId)}:${kind}`;
}

export function mintStackLifecycleRetryIdempotencyKey(
  stackId: string,
  jobId: string,
  kind: "deploy" | "provision",
): string {
  return mintJoinWizardIdempotencyKey(
    stackLifecycleRetryScope(stackId, jobId, kind),
  );
}

export function clearStackLifecycleRetryIdempotencyKey(
  stackId: string,
  jobId: string,
  kind: "deploy" | "provision",
  submittedKey: string,
): void {
  const storageKey = joinWizardIdempotencyStorageKey(
    stackLifecycleRetryScope(stackId, jobId, kind),
  );
  if (sessionStorage.getItem(storageKey) === submittedKey) {
    sessionStorage.removeItem(storageKey);
  }
}

export function clearJoinWizardIdempotencyKeys(stackId: string): void {
  if (!stackId) return;
  sessionStorage.removeItem(legacyJoinWizardIdempotencyStorageKey(stackId));
  for (const mode of ["join", "found"] as const) {
    sessionStorage.removeItem(joinWizardIdempotencyStorageKey(stackId, mode));
  }
}

export function clearAllWizardIdempotencyKeys(): void {
  const keysToRemove: string[] = [];
  for (let index = 0; index < sessionStorage.length; index += 1) {
    const key = sessionStorage.key(index);
    if (key?.startsWith(JOIN_WIZARD_IDEMPOTENCY_STORAGE_PREFIX)) {
      keysToRemove.push(key);
    }
  }
  for (const key of keysToRemove) {
    sessionStorage.removeItem(key);
  }
  sessionStorage.removeItem("stackCreationIdempotencyKey");
}

export function clearSubmittedJoinWizardIdempotencyKey(
  idempotencyKey: string,
): void {
  if (!idempotencyKey) return;
  const keysToRemove: string[] = [];
  for (let index = 0; index < sessionStorage.length; index += 1) {
    const key = sessionStorage.key(index);
    if (
      key?.startsWith(JOIN_WIZARD_IDEMPOTENCY_STORAGE_PREFIX) &&
      sessionStorage.getItem(key) === idempotencyKey
    ) {
      keysToRemove.push(key);
    }
  }
  for (const key of keysToRemove) {
    sessionStorage.removeItem(key);
  }
}
