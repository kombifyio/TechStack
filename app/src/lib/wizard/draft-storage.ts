/**
 * Wizard draft: the answers a person has entered, kept for this browser tab so
 * a reload does not start the creation flow over.
 *
 * The draft lives in sessionStorage only (gone when the tab closes) and never
 * holds a secret: passwords and values derived from a typed secret are
 * blanked before anything is written, and the recovery passphrase itself is
 * not part of the config at all. It ends when the creation is accepted or the
 * person chooses to leave.
 */
import type { StackConfig } from "./types";

const STORAGE_PREFIX = "techstack.wizardDraft.v1.";

export interface WizardDraft {
  step: number;
  config: StackConfig;
}

/** A deep copy of the config without anything that is or derives from a secret. */
export function withoutSecrets(config: StackConfig): StackConfig {
  const copy = JSON.parse(JSON.stringify(config)) as StackConfig;
  if (copy.serverProvisioning?.remote)
    copy.serverProvisioning.remote.sshPassword = "";
  if (copy.owner) copy.owner.recoveryPassphraseHash = "";
  return copy;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Lay a stored object over the current defaults so a field added after the
 * draft was written still has its default. Arrays and scalars of the draft win.
 */
export function mergeOverDefaults<T>(defaults: T, stored: unknown): T {
  if (!isRecord(defaults) || !isRecord(stored))
    return (stored ?? defaults) as T;
  const merged: Record<string, unknown> = { ...defaults };
  for (const [key, value] of Object.entries(stored)) {
    merged[key] =
      key in defaults ? mergeOverDefaults(defaults[key], value) : value;
  }
  return merged as T;
}

export function saveWizardDraft(
  scope: string,
  draft: WizardDraft,
  storage: Pick<Storage, "setItem"> | undefined = browserStorage(),
): void {
  try {
    storage?.setItem(
      STORAGE_PREFIX + scope,
      JSON.stringify({
        step: draft.step,
        config: withoutSecrets(draft.config),
      }),
    );
  } catch {
    // Storage can be full or blocked; the wizard keeps working without a draft.
  }
}

export function loadWizardDraft(
  scope: string,
  storage: Pick<Storage, "getItem"> | undefined = browserStorage(),
): WizardDraft | null {
  try {
    const raw = storage?.getItem(STORAGE_PREFIX + scope);
    if (!raw) return null;
    const parsed: unknown = JSON.parse(raw);
    if (!isRecord(parsed) || !isRecord(parsed.config)) return null;
    const step = Number(parsed.step);
    return {
      step: Number.isInteger(step) && step >= 1 ? step : 1,
      config: parsed.config as unknown as StackConfig,
    };
  } catch {
    return null;
  }
}

export function clearWizardDraft(
  scope: string,
  storage: Pick<Storage, "removeItem"> | undefined = browserStorage(),
): void {
  try {
    storage?.removeItem(STORAGE_PREFIX + scope);
  } catch {
    // Nothing to clear when storage is blocked.
  }
}

function browserStorage(): Storage | undefined {
  try {
    return typeof sessionStorage === "undefined" ? undefined : sessionStorage;
  } catch {
    return undefined;
  }
}
