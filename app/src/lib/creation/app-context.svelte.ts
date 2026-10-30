/**
 * The creation context of the Techstack app itself: the one place that connects
 * the creation steps to the app's stores.
 */
import { fromStore } from "svelte/store";
import { listWorkers } from "#lib/api/workers.js";
import { authStore } from "#lib/stores/auth.svelte.js";
import {
  features,
  getDefaultEnabled,
  loadFeatures,
} from "#lib/stores/features.js";
import { operatorExperience } from "#lib/stores/operatorExperience.svelte.js";
import { theme } from "#lib/stores/theme.js";
import type { CreationContext } from "./context";

export function appCreationContext(
  deploymentId: () => string | undefined = () => undefined,
): CreationContext {
  const flags = fromStore(features.allFeatures);
  return {
    auth: {
      get modeDetected() {
        return authStore.modeDetected;
      },
      get isAuthenticated() {
        return authStore.isAuthenticated;
      },
      get lane() {
        return authStore.deploymentMode === "saas" ? "saas" : "self-hosted";
      },
      get walletSource() {
        return authStore.walletSource;
      },
      get signedInAccount() {
        return {
          email: authStore.userEmail ?? "",
          displayName: authStore.userName ?? "",
          emailVerified: authStore.currentUser?.email_verified === true,
        };
      },
    },
    features: {
      all: features.allFeatures,
      ready: features.ready,
      loading: features.loading,
      errorKind: features.errorKind,
      load: (force) => loadFeatures(force),
      isEnabled: (flag) =>
        flags.current.get(flag)?.enabled ?? getDefaultEnabled(flag),
    },
    experience: {
      get level() {
        return operatorExperience.experience;
      },
      load: () => operatorExperience.load(),
    },
    appearance: theme.resolved,
    inventory: { listWorkers },
    get deploymentId() {
      return deploymentId();
    },
  };
}
