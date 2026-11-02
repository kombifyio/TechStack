/**
 * Techstack's create protection for the signed-in account, per managed provider
 * and region. Admission enforces the pause on its own; this read lets the
 * creation flow say why creation is paused and hold only the create action,
 * while reading, stopping and deleting existing servers stay available.
 */
import {
  getManagedCreationPause,
  type ManagedCreationPause,
} from "#lib/api/stacks.js";
import type { StackConfig } from "#lib/wizard/index.js";

export interface ManagedCreationScope {
  providerId: string;
  region: string;
}

/** The provider and region a managed create would use, or null for any other server source. */
export function managedCreationScope(
  config: StackConfig,
): ManagedCreationScope | null {
  if (config.serverProvisioning.mode !== "kombify-cloud" || !config.providerId)
    return null;
  // centron has one pool; Techstack resolves its region like the create does.
  const region =
    config.providerId === "ionos" ? (config.ionosDatacenter ?? "") : "";
  return { providerId: config.providerId, region };
}

const scopeKey = (scope: ManagedCreationScope) =>
  `${scope.providerId}\u0000${scope.region}`;

class ManagedCreationPauses {
  #states = $state<Record<string, ManagedCreationPause>>({});
  #inflight = new Map<string, Promise<void>>();
  #timers = new Map<string, ReturnType<typeof setTimeout>>();

  /** The last read; null while unknown or when the read failed (admission still decides). */
  get(scope: ManagedCreationScope | null): ManagedCreationPause | null {
    return scope ? (this.#states[scopeKey(scope)] ?? null) : null;
  }

  /** Whether creating a server in this scope is held right now. */
  holds(scope: ManagedCreationScope | null): boolean {
    const pause = this.get(scope);
    return pause !== null && pause.state !== "ready";
  }

  /** Read the scope again; a pause is read once more when its cooldown ends. */
  refresh(scope: ManagedCreationScope): Promise<void> {
    const key = scopeKey(scope);
    const running = this.#inflight.get(key);
    if (running) return running;
    const read = this.#read(scope, key).finally(() =>
      this.#inflight.delete(key),
    );
    this.#inflight.set(key, read);
    return read;
  }

  async #read(scope: ManagedCreationScope, key: string) {
    clearTimeout(this.#timers.get(key));
    this.#timers.delete(key);
    try {
      const pause = await getManagedCreationPause(
        scope.providerId,
        scope.region,
      );
      this.#states = { ...this.#states, [key]: pause };
      // A pause is read again when its cooldown ends; a create in flight or
      // being reconciled is read again until it settles.
      if (pause.state !== "ready") {
        const retryAt = Date.parse(pause.retry_at ?? "");
        const wait =
          pause.state === "paused" && Number.isFinite(retryAt)
            ? retryAt - Date.now() + 1_000
            : 15_000;
        this.#timers.set(
          key,
          setTimeout(() => void this.refresh(scope), Math.max(wait, 1_000)),
        );
      }
    } catch {
      const { [key]: _dropped, ...rest } = this.#states;
      this.#states = rest;
    }
  }
}

export const managedCreationPauses = new ManagedCreationPauses();
