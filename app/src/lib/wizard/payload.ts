/**
 * Identity preferences a run request carries, derived from the wizard answers.
 */

import {
  createDefaultIdentityConfig,
  type IdentityConfig,
  type ServicesConfig,
  type StackConfig,
} from "./types";

type StackConfigWithLegacyIdentity = Omit<
  StackConfig,
  "identity" | "services"
> & {
  identity?: Partial<IdentityConfig>;
  services: ServicesConfig & { pocketId?: boolean };
};

export function resolveIdentityConfig(config: StackConfig): IdentityConfig {
  const provided = (config as StackConfigWithLegacyIdentity).identity;
  const requiresPasskeys =
    provided?.requiresPasskeys === true ||
    (config.auth?.allowPasswordless ?? false);
  const homelabProvider = provided?.homelabProvider ?? "pocket-id";
  const toolProvider = provided?.toolProvider ?? "pocket-id";
  const backendCapability = requiresPasskeys
    ? "passkeys"
    : homelabProvider === "pocketbase"
      ? "pocketbase"
      : "external-identity";

  return {
    ...createDefaultIdentityConfig(),
    ...provided,
    toolProvider,
    homelabProvider,
    requiresPasskeys,
    backendCapability,
  };
}

export function deriveIdentityServices(config: StackConfig): IdentityConfig {
  const legacyConfig = config as StackConfigWithLegacyIdentity;
  const hasExplicitIdentity = Boolean(legacyConfig.identity);
  const identity = resolveIdentityConfig(config);

  legacyConfig.identity = identity;
  legacyConfig.services.pocketId =
    identity.toolProvider === "pocket-id" ||
    identity.homelabProvider === "pocket-id" ||
    identity.requiresPasskeys ||
    legacyConfig.services.pocketId === true;
  legacyConfig.services.pocketbase =
    identity.homelabProvider === "pocketbase" ||
    (!hasExplicitIdentity && legacyConfig.services.pocketbase === true);

  return identity;
}

/**
 * Retained for legacy callers preparing identity preferences. Goals remain
 * intent; only the pinned StackKits CLI expands them into services.
 */
export function deriveServicesFromGoals(config: StackConfig): void {
  deriveIdentityServices(config);
}
