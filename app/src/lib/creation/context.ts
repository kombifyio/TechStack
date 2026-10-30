/**
 * What the creation steps need from their surroundings. A frame provides it, so
 * a step mounts wherever a frame can answer these and imports no app store,
 * route or worker list of its own.
 */
import { getContext, setContext } from "svelte";
import type { Readable } from "svelte/store";
import type { Worker } from "#lib/api/workers.js";
import type { FeatureFlag, FeaturesErrorKind } from "#lib/stores/features.js";
import type { SelfDisclosureExperience } from "#lib/api/operatorProfile.js";
import type { WizardDeploymentLane } from "#lib/wizard/types.js";
import type { SignedInOwnerAccount } from "#lib/wizard/session-owner.js";

export interface CreationContext {
  auth: {
    readonly modeDetected: boolean;
    readonly isAuthenticated: boolean;
    readonly lane: WizardDeploymentLane;
    /** "kombify": this surface reaches servers with a Wallet item. */
    readonly walletSource: string;
    readonly signedInAccount: SignedInOwnerAccount;
  };
  features: {
    all: Readable<Map<string, FeatureFlag>>;
    ready: Readable<boolean>;
    loading: Readable<boolean>;
    errorKind: Readable<FeaturesErrorKind>;
    load(force?: boolean): Promise<unknown>;
    isEnabled(flag: string): boolean;
  };
  experience: {
    readonly level: SelfDisclosureExperience | null;
    load(): Promise<unknown>;
  };
  appearance: Readable<"light" | "dark">;
  inventory: {
    listWorkers(): Promise<Worker[]>;
  };
  /** The deployment this frame works on, when there is one. */
  readonly deploymentId: string | undefined;
}

const CONTEXT_KEY = Symbol("creation-context");

export function provideCreationContext(context: CreationContext): void {
  setContext(CONTEXT_KEY, context);
}

export function useCreationContext(): CreationContext {
  const context = getContext<CreationContext | undefined>(CONTEXT_KEY);
  if (!context) throw new Error("A creation step needs a creation frame.");
  return context;
}
