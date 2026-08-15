/**
 * Deployment Mode Store
 *
 * Detects and manages the display mode of kombify-TechStack:
 * - 'self-hosted': Full standalone application with left sidebar
 * - 'saas-embedded': Embedded in kombify Cloud portal with inline tabs
 *
 * Embedded mode is only allowed when the backend reports deployment_mode="saas".
 * A self-hosted backend will always show the standalone UI regardless of iframe
 * or URL parameters.
 */

import { writable, derived } from "svelte/store";
import { browser } from "$app/environment";

export type DeploymentMode = "self-hosted" | "saas-embedded";
export type DataPlaneMode = "same-origin" | "gateway";

interface DeploymentConfig {
  /**
   * UI mode. SaaS standalone intentionally keeps the self-hosted/full-shell UI
   * while using the gateway data plane below.
   */
  mode: DeploymentMode;
  /** API transport authority for data-plane routes. */
  dataPlane: DataPlaneMode;
  portalUrl?: string; // URL of the parent portal (if embedded)
  instanceName?: string; // Name of this instance
  features: {
    showSidebar: boolean;
    showInlineTabs: boolean;
    showFullAppButton: boolean;
  };
}

const defaultConfig: DeploymentConfig = {
  mode: "self-hosted",
  dataPlane: "same-origin",
  instanceName: "kombify-TechStack",
  features: {
    showSidebar: true,
    showInlineTabs: false,
    showFullAppButton: false,
  },
};

const saasEmbeddedConfig: DeploymentConfig = {
  mode: "saas-embedded",
  dataPlane: "gateway",
  portalUrl: "/",
  instanceName: "kombify-TechStack",
  features: {
    showSidebar: false,
    showInlineTabs: true,
    showFullAppButton: true,
  },
};

const saasStandaloneConfig: DeploymentConfig = {
  ...defaultConfig,
  dataPlane: "gateway",
};

/**
 * Create the deployment mode store
 */
function createDeploymentStore() {
  const { subscribe, set, update } = writable<DeploymentConfig>(defaultConfig);

  return {
    subscribe,

    /**
     * Initialize deployment mode detection.
     * Accepts the backend deployment mode (from auth store or API) and only
     * allows embedded UI when the backend is running in SaaS mode.
     *
     * @param backendDeploymentMode - "self-hosted" or "saas" from the backend
     */
    init: async (backendDeploymentMode?: string) => {
      if (!browser) return;

      // 1. Determine backend mode (fetch if not provided)
      let backendMode: "self-hosted" | "saas" = "self-hosted";
      if (backendDeploymentMode === "saas") {
        backendMode = "saas";
      } else if (!backendDeploymentMode) {
        try {
          const res = await fetch("/api/v1/auth/mode");
          if (res.ok) {
            const data = await res.json();
            if (data?.data?.deployment_mode === "saas") {
              backendMode = "saas";
            }
          }
        } catch {
          // Backend unreachable — default to self-hosted
        }
      }

      // 2. Client-side signals (only relevant when backend is SaaS)
      const urlParams = new URLSearchParams(window.location.search);
      const isEmbeddedParam = urlParams.get("embedded") === "true";
      const isInIframe = window.parent !== window;
      const storedOverride = localStorage.getItem("techstack-mode");
      const devOverride: DeploymentMode | null =
        storedOverride === "self-hosted" || storedOverride === "saas-embedded"
          ? storedOverride
          : null;

      if (storedOverride && !devOverride) {
        localStorage.removeItem("techstack-mode");
      }

      let detectedMode: DeploymentMode = "self-hosted";

      if (devOverride) {
        detectedMode =
          devOverride === "saas-embedded" && backendMode === "saas"
            ? "saas-embedded"
            : "self-hosted";
      } else if (backendMode === "saas" && (isEmbeddedParam || isInIframe)) {
        detectedMode = "saas-embedded";
      }

      if (detectedMode === "saas-embedded") {
        set(saasEmbeddedConfig);
      } else if (backendMode === "saas") {
        set(saasStandaloneConfig);
      } else {
        set(defaultConfig);
      }
    },

    /**
     * Manually set the deployment mode (useful for development/testing)
     */
    setMode: (mode: DeploymentMode) => {
      if (browser) {
        localStorage.setItem("techstack-mode", mode);
      }
      set(mode === "saas-embedded" ? saasEmbeddedConfig : defaultConfig);
    },

    /**
     * Clear any stored mode preference
     */
    clearOverride: () => {
      if (browser) {
        localStorage.removeItem("techstack-mode");
      }
    },

    /**
     * Toggle between modes (for development)
     */
    toggle: () => {
      update((config) => {
        const newMode: DeploymentMode =
          config.mode === "self-hosted" ? "saas-embedded" : "self-hosted";
        if (browser) {
          localStorage.setItem("techstack-mode", newMode);
        }
        return newMode === "saas-embedded" ? saasEmbeddedConfig : defaultConfig;
      });
    },
  };
}

export const deploymentMode = createDeploymentStore();

// Derived stores for convenience
export const isSelfHosted = derived(
  deploymentMode,
  ($config) => $config.mode === "self-hosted",
);

export const isSaaSEmbedded = derived(
  deploymentMode,
  ($config) => $config.mode === "saas-embedded",
);

export const isGatewayDataPlane = derived(
  deploymentMode,
  ($config) => $config.dataPlane === "gateway",
);

export const showSidebar = derived(
  deploymentMode,
  ($config) => $config.features.showSidebar,
);

export const showInlineTabs = derived(
  deploymentMode,
  ($config) => $config.features.showInlineTabs,
);

export const showFullAppButton = derived(
  deploymentMode,
  ($config) => $config.features.showFullAppButton,
);
