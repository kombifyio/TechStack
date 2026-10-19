import type {
  AddManagedRuntimeServerRequest,
  StackKitRestoreBinding,
} from "#lib/api/stacks.js";

const RECORD_VERSION = "techstack/add-managed-runtime-browser-key/v1";
const STORAGE_PREFIX = "creating:add-server:";
const STACK_ACTION_STORAGE_PREFIX = "techstack:stack-action:v1:";

export interface CanonicalManagedRuntimeIntent {
  stack_id: string;
  provider_id: string;
  provider_region: string;
  node_role: string;
  runtime_offering_id: string;
  stackkit: string;
  services: string[];
}

export interface ManagedRuntimeIdempotencyAttempt {
  intent: CanonicalManagedRuntimeIntent;
  key: string;
}

export interface ManagedRuntimeIdempotencyOutcome {
  status: number;
  retryable?: boolean;
}

export type StackIdempotentAction = "provision" | "deploy" | "recreate";

export interface StackActionIdempotencyAttempt {
  storageKey: string;
  key: string;
}

interface StoredManagedRuntimeIdempotency {
  version: typeof RECORD_VERSION;
  intent: CanonicalManagedRuntimeIntent;
  key: string;
}

type KeyFactory = () => string;

function normalizedLower(value: string | undefined): string {
  return (value || "").trim().toLowerCase();
}

function canonicalProvider(value: string): string {
  const trimmed = value.trim();
  switch (trimmed.toLowerCase()) {
    case "centron":
      return "centron";
    case "ionos":
      return "ionos";
    default:
      return trimmed;
  }
}

function canonicalIonosRegion(value: string | undefined): string {
  const normalized = normalizedLower(value)
    .replace(/\\/g, "/")
    .replace(/_/g, "/");
  switch (normalized) {
    case "":
    case "default":
    case "de/fra":
    case "de-fra":
    case "fra":
    case "frankfurt":
      return "de/fra";
    case "de/txl":
    case "de-txl":
    case "txl":
    case "berlin":
      return "de/txl";
    case "us/ewr":
    case "us-ewr":
    case "ewr":
    case "newark":
      return "us/ewr";
    case "us/las":
    case "us-las":
    case "las":
    case "las-vegas":
      return "us/las";
    case "de/fra/2":
    case "de-fra-2":
    case "fra2":
    case "frankfurt-2":
      return "de/fra/2";
    default:
      return "de/fra";
  }
}

function canonicalNodeRole(value: string | undefined): string {
  switch (normalizedLower(value)) {
    case "foundation":
    case "main":
      return "foundation";
    case "storage":
      return "storage";
    default:
      return "worker";
  }
}

function canonicalOffering(value: string | undefined): string {
  const trimmed = (value || "").trim();
  switch (trimmed.toLowerCase()) {
    case "monthly-runtime-standard":
      return "monthly-runtime-standard";
    case "monthly-runtime-premium":
      return "monthly-runtime-premium";
    case "monthly-runtime-large":
      return "monthly-runtime-large";
    default:
      return trimmed;
  }
}

function canonicalStackKit(value: string | undefined): string {
  const trimmed = (value || "").trim();
  switch (trimmed.toLowerCase()) {
    case "cloud":
    case "cloudkit":
      return "cloud-kit";
    case "basement":
    case "basementkit":
      return "basement-kit";
    default:
      return trimmed;
  }
}

function canonicalService(value: string): string {
  const normalized = normalizedLower(value).replace(/-/g, "_");
  switch (normalized) {
    case "pocketid":
    case "pocket_id":
    case "pocketbase_identity":
    case "identity":
      return "pocket_id";
    default:
      return normalized;
  }
}

function canonicalServices(values: string[] | undefined): string[] {
  return Array.from(
    new Set((values || []).map(canonicalService).filter(Boolean)),
  ).sort();
}

export function canonicalizeManagedRuntimeIntent(
  stackId: string,
  request: AddManagedRuntimeServerRequest,
): CanonicalManagedRuntimeIntent {
  const provider = canonicalProvider(request.provider_id);
  const requestedRegion =
    request.ionos_datacenter?.trim() || request.provider_region || "";
  return {
    stack_id: stackId.trim(),
    provider_id: provider,
    provider_region:
      provider === "ionos" ? canonicalIonosRegion(requestedRegion) : "",
    node_role: canonicalNodeRole(request.node_role),
    runtime_offering_id: canonicalOffering(request.runtime_offering_id),
    stackkit: canonicalStackKit(request.stackkit),
    services: canonicalServices(request.services),
  };
}

function storageKey(intent: CanonicalManagedRuntimeIntent): string {
  return `${STORAGE_PREFIX}${encodeURIComponent(intent.stack_id)}:idempotency`;
}

function serializedIntent(intent: CanonicalManagedRuntimeIntent): string {
  return JSON.stringify(intent);
}

function stackActionStorageKey(
  stackId: string,
  action: StackIdempotentAction,
): string {
  return `${STACK_ACTION_STORAGE_PREFIX}${encodeURIComponent(stackId)}:${action}:idempotency`;
}

function validOpaqueKey(value: unknown): value is string {
  if (typeof value !== "string" || value === "" || value.trim() !== value) {
    return false;
  }
  if (value.includes(",")) return false;
  for (const character of value) {
    const codePoint = character.codePointAt(0) ?? 0;
    if (codePoint <= 0x1f || (codePoint >= 0x7f && codePoint <= 0x9f)) {
      return false;
    }
  }
  return new TextEncoder().encode(value).byteLength <= 256;
}

function readStored(
  storage: Storage,
  key: string,
): StoredManagedRuntimeIdempotency | null {
  const raw = storage.getItem(key);
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as Partial<StoredManagedRuntimeIdempotency>;
    if (
      parsed.version !== RECORD_VERSION ||
      !parsed.intent ||
      !validOpaqueKey(parsed.key)
    ) {
      return null;
    }
    return parsed as StoredManagedRuntimeIdempotency;
  } catch {
    return null;
  }
}

function browserGeneratedKey(): string {
  if (typeof globalThis.crypto?.randomUUID !== "function") {
    throw new Error(
      "Secure browser idempotency key generation is unavailable.",
    );
  }
  return globalThis.crypto.randomUUID();
}

export function getOrCreateStackActionIdempotency(
  storage: Storage,
  stackId: string,
  action: StackIdempotentAction,
  createKey: KeyFactory = browserGeneratedKey,
): StackActionIdempotencyAttempt {
  const canonicalStackId = stackId.trim();
  if (!canonicalStackId) {
    throw new Error("A stack ID is required for an idempotent stack action.");
  }

  const recordKey = stackActionStorageKey(canonicalStackId, action);
  const stored = storage.getItem(recordKey);
  if (validOpaqueKey(stored)) {
    return { storageKey: recordKey, key: stored };
  }

  const key = createKey();
  if (!validOpaqueKey(key)) {
    throw new Error("Secure browser idempotency key generation failed.");
  }
  storage.setItem(recordKey, key);
  return { storageKey: recordKey, key };
}

export function getOrCreateManagedRuntimeIdempotency(
  storage: Storage,
  stackId: string,
  request: AddManagedRuntimeServerRequest,
  createKey: KeyFactory = browserGeneratedKey,
): ManagedRuntimeIdempotencyAttempt {
  const intent = canonicalizeManagedRuntimeIntent(stackId, request);
  if (!intent.stack_id) {
    throw new Error("A stack ID is required for managed runtime creation.");
  }

  const recordKey = storageKey(intent);
  const stored = readStored(storage, recordKey);
  if (stored && serializedIntent(stored.intent) === serializedIntent(intent)) {
    return { intent, key: stored.key };
  }

  const key = createKey();
  if (!validOpaqueKey(key)) {
    throw new Error("Secure browser idempotency key generation failed.");
  }
  const record: StoredManagedRuntimeIdempotency = {
    version: RECORD_VERSION,
    intent,
    key,
  };
  storage.setItem(recordKey, JSON.stringify(record));
  return { intent, key };
}

function isTerminal(outcome: ManagedRuntimeIdempotencyOutcome): boolean {
  return (
    (outcome.status === 202 && outcome.retryable !== true) ||
    outcome.status === 422 ||
    (outcome.status === 403 && outcome.retryable !== true) ||
    (outcome.status === 409 && outcome.retryable !== true)
  );
}

/**
 * Remove only the exact attempt that settled. A late response for an older
 * semantic intent must not clear a newer key created while it was in flight.
 */
export function settleManagedRuntimeIdempotency(
  storage: Storage,
  attempt: ManagedRuntimeIdempotencyAttempt,
  outcome: ManagedRuntimeIdempotencyOutcome,
): void {
  if (!isTerminal(outcome)) return;

  const recordKey = storageKey(attempt.intent);
  const stored = readStored(storage, recordKey);
  if (
    !stored ||
    stored.key !== attempt.key ||
    serializedIntent(stored.intent) !== serializedIntent(attempt.intent)
  ) {
    return;
  }
  storage.removeItem(recordKey);
}

export function settleStackActionIdempotency(
  storage: Storage,
  attempt: StackActionIdempotencyAttempt,
  outcome: ManagedRuntimeIdempotencyOutcome,
): void {
  if (!isTerminal(outcome)) return;

  if (storage.getItem(attempt.storageKey) === attempt.key) {
    storage.removeItem(attempt.storageKey);
  }
}

export interface RestoreActivationBrowserScope {
  actorId: string;
  tenantId: string;
  stackId: string;
  nodeId: string;
}

export interface RestoreActivationBrowserIntent extends RestoreActivationBrowserScope {
  version: "techstack/restore-activation-browser-intent/v1";
  key: string;
  binding: StackKitRestoreBinding;
  jobId?: string;
  keySHA256?: string;
  requestDigest?: string;
  observationJobId?: string;
  observationOperation?: "backup_restore_activate" | "backup_restore_recover";
}

function restoreIntentKey(scope: RestoreActivationBrowserScope): string {
  if (!scope.actorId || !scope.stackId || !scope.nodeId)
    throw new Error(
      "Original restore request requires its signed-in owner and node",
    );
  return `${STACK_ACTION_STORAGE_PREFIX}${encodeURIComponent(JSON.stringify([scope.actorId, scope.tenantId, scope.stackId, scope.nodeId]))}:restore-activate:pending`;
}

/** Nonsecret browser correlation; all mutation authority remains server-owned. */
export function readRestoreActivationIntent(
  storage: Storage,
  scope: RestoreActivationBrowserScope,
): RestoreActivationBrowserIntent | undefined {
  const raw = storage.getItem(restoreIntentKey(scope));
  if (!raw) return;
  let value: RestoreActivationBrowserIntent;
  try {
    value = JSON.parse(raw);
  } catch {
    throw new Error("Original restore request correlation is unreadable");
  }
  const digest = (candidate: unknown) =>
    typeof candidate === "string" && /^sha256:[a-f0-9]{64}$/.test(candidate);
  if (
    !value ||
    value.version !== "techstack/restore-activation-browser-intent/v1" ||
    !validOpaqueKey(value.key) ||
    value.key.length > 128 ||
    value.actorId !== scope.actorId ||
    value.tenantId !== scope.tenantId ||
    value.stackId !== scope.stackId ||
    value.nodeId !== scope.nodeId ||
    !value.binding ||
    value.binding.stackId !== scope.stackId ||
    value.binding.nodeId !== scope.nodeId ||
    !value.binding.agentId ||
    !value.binding.instanceId ||
    !value.binding.ownerRef ||
    !digest(value.binding.restoreResultId) ||
    !digest(value.binding.planHash) ||
    !digest(value.binding.anchorId)
  )
    throw new Error(
      "Original restore request correlation does not match this owner and node",
    );
  return value;
}

export function retainRestoreActivationIntent(
  storage: Storage,
  scope: RestoreActivationBrowserScope,
  binding: StackKitRestoreBinding,
): RestoreActivationBrowserIntent {
  if (readRestoreActivationIntent(storage, scope))
    throw new Error(
      "Resolve the original restore request before starting another",
    );
  const value: RestoreActivationBrowserIntent = {
    ...scope,
    version: "techstack/restore-activation-browser-intent/v1",
    key: browserGeneratedKey(),
    binding: { ...binding },
  };
  storage.setItem(restoreIntentKey(scope), JSON.stringify(value));
  return value;
}

export function updateRestoreActivationIntent(
  storage: Storage,
  value: RestoreActivationBrowserIntent,
): void {
  const current = readRestoreActivationIntent(storage, value);
  if (
    !current ||
    current.key !== value.key ||
    JSON.stringify(current.binding) !== JSON.stringify(value.binding)
  )
    throw new Error("Original restore request correlation changed");
  storage.setItem(restoreIntentKey(value), JSON.stringify(value));
}

export function settleRestoreActivationIntent(
  storage: Storage,
  value: RestoreActivationBrowserIntent,
): void {
  const current = readRestoreActivationIntent(storage, value);
  if (
    current?.key === value.key &&
    current.jobId === value.jobId &&
    current.requestDigest === value.requestDigest
  )
    storage.removeItem(restoreIntentKey(value));
}
