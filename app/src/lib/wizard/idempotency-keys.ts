export type WizardJoinAdditionMode = "join" | "found";

const JOIN_WIZARD_IDEMPOTENCY_STORAGE_PREFIX = "wizardJoinIdempotencyKey:";

const STACK_CREATION_IDEMPOTENCY_STORAGE_KEY = "stackCreationIdempotencyKey";

/** The attempt of a new deployment in this tab; a replay keeps its key. */
export function mintStackCreationIdempotencyKey(): string {
  const existing = sessionStorage.getItem(
    STACK_CREATION_IDEMPOTENCY_STORAGE_KEY,
  );
  if (existing) return existing;
  const generated =
    typeof crypto?.randomUUID === "function"
      ? crypto.randomUUID()
      : `create-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  sessionStorage.setItem(STACK_CREATION_IDEMPOTENCY_STORAGE_KEY, generated);
  return generated;
}

export function clearStackCreationIdempotencyKey(): void {
  sessionStorage.removeItem(STACK_CREATION_IDEMPOTENCY_STORAGE_KEY);
}

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

function extendWizardIdempotencyStorageKey(stackId: string): string {
  return `${JOIN_WIZARD_IDEMPOTENCY_STORAGE_PREFIX}extend:${stackId}`;
}

/**
 * The key of an extend run ("add use cases"). The same request retried (a
 * dropped connection, a reload) keeps its key, so the backend replays the
 * attempt; a changed request (other use cases, a newer recommendation) gets a
 * fresh key, because the backend answers a different payload on a used key
 * with a conflict.
 */
export function mintExtendWizardIdempotencyKey(
  stackId: string,
  fingerprint: string,
): string {
  const storageKey = extendWizardIdempotencyStorageKey(stackId);
  try {
    const stored = JSON.parse(sessionStorage.getItem(storageKey) ?? "null") as {
      fingerprint?: unknown;
      key?: unknown;
    } | null;
    if (
      stored?.fingerprint === fingerprint &&
      typeof stored.key === "string" &&
      stored.key.trim()
    ) {
      return stored.key;
    }
  } catch {
    // An unreadable stored key is replaced below.
  }
  const generated =
    typeof crypto?.randomUUID === "function"
      ? crypto.randomUUID()
      : `extend-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  try {
    sessionStorage.setItem(
      storageKey,
      JSON.stringify({ fingerprint, key: generated }),
    );
  } catch {
    // Without storage a retry mints a new key; the server refuses a stale one.
  }
  return generated;
}

/** An extend run reached a definitive outcome; the next attempt starts fresh. */
export function clearExtendWizardIdempotencyKey(stackId: string): void {
  if (!stackId) return;
  sessionStorage.removeItem(extendWizardIdempotencyStorageKey(stackId));
}
