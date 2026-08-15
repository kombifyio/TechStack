/**
 * OwnerStepState - state and validation for the wizard "login" step (step 5).
 *
 * Holds everything that is NOT part of StackConfig: recovery passphrase
 * plaintexts (never serialized), the admin password confirmation, hashing
 * status, and the kombify Cloud link status for the cloud-linked owner
 * source. StackConfig itself stays owned by the wizard; the class reaches it
 * through a getter so config reassignment in the wizard stays reactive.
 */

import {
  hashPassphrase,
  MIN_RECOVERY_PASSPHRASE_LENGTH,
  scoreStrength,
} from "$lib/wizard/argon2";
import {
  getCloudLinkStatus,
  startCloudLink,
  unlinkCloud,
  type CloudLinkStatus,
} from "$lib/api/cloudlink";
import type { StackConfig } from "./types";

const CLOUD_LINK_POLL_INTERVAL_MS = 3000;
const CLOUD_LINK_POLL_MAX_MS = 2 * 60 * 1000;

export type CloudLinkUiState =
  | "idle"
  | "starting"
  | "waiting"
  | "linked"
  | "unavailable"
  | "error";

export class OwnerStepState {
  recoveryPassphrase = $state("");
  recoveryPassphraseConfirm = $state("");
  adminPasswordConfirm = $state("");
  recoveryHashError = $state<string | null>(null);
  isHashingRecovery = $state(false);

  cloudLink = $state<CloudLinkStatus>({ linked: false });
  cloudLinkState = $state<CloudLinkUiState>("idle");
  cloudLinkError = $state<string | null>(null);
  cloudLinkGuidance = $state<string | null>(null);

  #getConfig: () => StackConfig;
  #pollTimer: ReturnType<typeof setInterval> | null = null;
  #pollStartedAt = 0;

  constructor(getConfig: () => StackConfig) {
    this.#getConfig = getConfig;
  }

  get config(): StackConfig {
    return this.#getConfig();
  }

  // -- derived validation ---------------------------------------------------

  readonly passwordsMatch = $derived(
    this.config.admin.password === this.adminPasswordConfirm,
  );
  readonly recoveryPassphrasesMatch = $derived(
    this.recoveryPassphrase.length > 0 &&
      this.recoveryPassphrase === this.recoveryPassphraseConfirm,
  );
  readonly recoveryPassphraseLongEnough = $derived(
    this.recoveryPassphrase.length >= MIN_RECOVERY_PASSPHRASE_LENGTH,
  );
  readonly hasRecoveryHash = $derived(
    this.config.owner.recoveryPassphraseHash.length > 0,
  );
  readonly recoveryStrength = $derived(scoreStrength(this.recoveryPassphrase));
  readonly hasLoginMethod = $derived(
    this.config.auth.requirePassword ||
      this.config.auth.requireMfa ||
      this.config.auth.allowPasswordless,
  );
  readonly bootstrapAuto = $derived(this.config.owner.bootstrapMode === "auto");
  readonly bootstrapSkipped = $derived(
    this.config.owner.bootstrapMode === "none",
  );
  readonly cloudLinkReady = $derived(
    this.cloudLink.linked && this.cloudLink.email_verified === true,
  );

  /**
   * Step-5 gate for navigation/create. Mirrors the historical adminIsValid:
   * auto and none bootstraps are always valid; custom bootstraps need a
   * usable owner identity plus consistent login-method inputs.
   */
  adminIsValid(): boolean {
    const config = this.config;
    if (
      config.owner.bootstrapMode === "auto" ||
      config.owner.bootstrapMode === "none"
    ) {
      return true;
    }
    if (!config.owner.source) return false;
    // The legacy custom "cloud" source has no implementation; it only occurs
    // in stale configs and must not pass validation.
    if (config.owner.source === "cloud") return false;
    if (config.owner.source === "cloud-linked" && !this.cloudLinkReady) {
      return false;
    }
    if (config.owner.source === "local" && !config.owner.email.trim()) {
      return false;
    }

    // If no login method selected, the StackKit owner handoff handles activation.
    if (!this.hasLoginMethod) return true;

    const needsEmail =
      config.owner.source === "local" ? config.owner.email.trim() : "seeded";
    if (config.auth.requirePassword) {
      if (!needsEmail) return false;
      if (!config.admin.password || !this.adminPasswordConfirm) return false;
      if (!this.passwordsMatch) return false;
    }
    if (config.auth.allowPasswordless && !config.auth.requirePassword) {
      if (!needsEmail) return false;
    }
    return true;
  }

  /** Human-readable step-5 validation errors (shown after interaction). */
  ownerValidationErrors(): string[] {
    const config = this.config;
    const errors: string[] = [];
    if (config.owner.bootstrapMode !== "custom") return errors;

    if (config.owner.source === "cloud") {
      errors.push(
        "The selected owner source is no longer supported. Choose a local owner or link your kombify Cloud profile.",
      );
    }
    if (config.owner.source === "cloud-linked" && !this.cloudLinkReady) {
      errors.push(
        "Connect your kombify Cloud profile (with a verified email) to use it as the owner",
      );
    }
    if (config.owner.source === "local" && !config.owner.email.trim()) {
      errors.push("Owner email is required for a custom owner override");
    }
    if (this.recoveryPassphrase || this.recoveryPassphraseConfirm) {
      if (!this.recoveryPassphraseLongEnough) {
        errors.push(
          `Recovery passphrase must be at least ${MIN_RECOVERY_PASSPHRASE_LENGTH} characters`,
        );
      }
      if (this.recoveryPassphraseConfirm && !this.recoveryPassphrasesMatch) {
        errors.push("Recovery passphrases do not match");
      }
    }
    if (this.recoveryHashError) {
      errors.push(this.recoveryHashError);
    }
    if (config.auth.requirePassword) {
      if (config.owner.source === "local" && !config.owner.email.trim()) {
        errors.push("Email is required for password authentication");
      }
      if (!config.admin.password) {
        errors.push("Password is required");
      }
      if (!this.adminPasswordConfirm) {
        errors.push("Please confirm your password");
      }
      if (
        config.admin.password &&
        this.adminPasswordConfirm &&
        !this.passwordsMatch
      ) {
        errors.push("Passwords do not match");
      }
    }
    if (
      config.auth.allowPasswordless &&
      !config.auth.requirePassword &&
      config.owner.source === "local" &&
      !config.owner.email.trim()
    ) {
      errors.push("Email is required for passwordless authentication");
    }
    return errors;
  }

  // -- owner source actions --------------------------------------------------

  selectOwnerSource(source: "local" | "cloud-linked") {
    this.config.owner.source = source;
    this.config.owner.bootstrapMode = "custom";
    if (source === "cloud-linked") {
      // Identity derives server-side from the verified link; local fields
      // must not ride along in the payload.
      this.config.owner.username = "";
      this.config.owner.email = "";
      this.config.owner.displayName = "";
    }
  }

  useAutoOwnerBootstrap() {
    const config = this.config;
    config.owner.bootstrapMode = "auto";
    config.owner.source = "cloud";
    config.owner.username = "";
    config.owner.email = "";
    config.owner.displayName = "";
    config.owner.recoveryMaterialRef ||= "techstack://recovery/stacks/homelab";
    this.resetRecoveryHash();
    this.recoveryPassphrase = "";
    this.recoveryPassphraseConfirm = "";
    config.auth.requirePassword = false;
    config.auth.requireMfa = false;
    config.auth.allowPasswordless = false;
  }

  useCustomOwnerBootstrap() {
    this.config.owner.bootstrapMode = "custom";
    this.config.owner.source = "local";
  }

  togglePasswordAuth() {
    this.config.auth.requirePassword = !this.config.auth.requirePassword;
  }

  toggleMfaAuth() {
    this.config.auth.requireMfa = !this.config.auth.requireMfa;
  }

  togglePasswordlessAuth() {
    this.config.auth.allowPasswordless = !this.config.auth.allowPasswordless;
  }

  // -- recovery passphrase ---------------------------------------------------

  resetRecoveryHash = () => {
    this.config.owner.recoveryPassphraseHash = "";
    this.recoveryHashError = null;
  };

  syncRecoveryHash = async (): Promise<boolean> => {
    const config = this.config;
    if (
      config.owner.bootstrapMode === "auto" ||
      config.owner.bootstrapMode === "none"
    ) {
      this.resetRecoveryHash();
      return true;
    }
    if (!this.recoveryPassphrase && !this.recoveryPassphraseConfirm) {
      this.resetRecoveryHash();
      return true;
    }
    if (!this.recoveryPassphrase || !this.recoveryPassphraseConfirm) {
      this.resetRecoveryHash();
      return false;
    }
    if (!this.recoveryPassphraseLongEnough || !this.recoveryPassphrasesMatch) {
      this.resetRecoveryHash();
      return false;
    }
    if (config.owner.recoveryPassphraseHash) {
      return true;
    }

    this.isHashingRecovery = true;
    this.recoveryHashError = null;
    try {
      config.owner.recoveryPassphraseHash = await hashPassphrase(
        this.recoveryPassphrase,
      );
      return true;
    } catch (error) {
      config.owner.recoveryPassphraseHash = "";
      this.recoveryHashError =
        error instanceof Error
          ? error.message
          : "Could not hash the recovery passphrase.";
      return false;
    } finally {
      this.isHashingRecovery = false;
    }
  };

  // -- cloud link flow --------------------------------------------------------

  refreshCloudLink = async (): Promise<void> => {
    try {
      this.cloudLink = await getCloudLinkStatus();
      if (this.cloudLink.linked) {
        this.cloudLinkState = "linked";
        this.stopCloudLinkPolling();
      } else if (this.cloudLinkState === "linked") {
        this.cloudLinkState = "idle";
      }
    } catch {
      // Status polling is best-effort; connect errors are surfaced separately.
    }
  };

  /**
   * Starts the PKCE flow and opens the hosted login. Returns the popup handle
   * (null when blocked); the card offers a new-tab fallback so the wizard tab
   * — and with it the in-memory wizard state — always stays alive.
   */
  connectCloudLink = async (): Promise<{
    authorizationUrl: string;
    popup: Window | null;
  } | null> => {
    this.cloudLinkError = null;
    this.cloudLinkGuidance = null;
    this.cloudLinkState = "starting";
    try {
      const { authorization_url } = await startCloudLink();
      const popup = window.open(
        authorization_url,
        "kombify-cloud-link",
        "popup,width=480,height=720",
      );
      this.cloudLinkState = "waiting";
      this.startCloudLinkPolling();
      return { authorizationUrl: authorization_url, popup };
    } catch (error) {
      const detail = error as {
        message?: string;
        details?: { reason_code?: string; user_guidance?: { body?: string } };
      };
      if (detail?.details?.reason_code === "cloud_oidc_not_configured") {
        this.cloudLinkState = "unavailable";
        this.cloudLinkGuidance =
          detail.details.user_guidance?.body ??
          "This instance has no kombify Cloud login configured.";
      } else {
        this.cloudLinkState = "error";
        this.cloudLinkError =
          detail?.message ?? "Could not start the kombify Cloud link.";
      }
      return null;
    }
  };

  /** Handles the completion message posted by /auth/cloud-link-complete. */
  handleCloudLinkMessage = (event: MessageEvent) => {
    // Defense in depth: only the same-origin completion page may drive the
    // card state (the handler is harmless either way - it only re-reads
    // server truth - but a foreign frame should not toggle UI states).
    if (event.origin !== window.location.origin) return;
    const data = event.data as {
      type?: string;
      status?: string;
      reason?: string;
    };
    if (!data || data.type !== "kombify:cloud-link") return;
    if (data.status === "ok") {
      void this.refreshCloudLink();
    } else {
      this.cloudLinkState = "error";
      this.cloudLinkError = cloudLinkReasonMessage(data.reason);
      this.stopCloudLinkPolling();
    }
  };

  disconnectCloudLink = async (): Promise<void> => {
    try {
      await unlinkCloud();
    } finally {
      this.cloudLink = { linked: false };
      this.cloudLinkState = "idle";
      if (this.config.owner.source === "cloud-linked") {
        this.useCustomOwnerBootstrap();
      }
    }
  };

  startCloudLinkPolling() {
    this.stopCloudLinkPolling();
    this.#pollStartedAt = Date.now();
    this.#pollTimer = setInterval(() => {
      if (Date.now() - this.#pollStartedAt > CLOUD_LINK_POLL_MAX_MS) {
        this.stopCloudLinkPolling();
        if (this.cloudLinkState === "waiting") {
          this.cloudLinkState = "idle";
        }
        return;
      }
      void this.refreshCloudLink();
    }, CLOUD_LINK_POLL_INTERVAL_MS);
  }

  stopCloudLinkPolling() {
    if (this.#pollTimer) {
      clearInterval(this.#pollTimer);
      this.#pollTimer = null;
    }
  }

  dispose() {
    this.stopCloudLinkPolling();
  }
}

export function cloudLinkReasonMessage(reason?: string): string {
  switch (reason) {
    case "email_unverified":
      return "The kombify Cloud email is not verified. Verify it in your Cloud account, then link again.";
    case "email_missing":
      return "The kombify Cloud profile has no email address.";
    case "state_expired":
      return "The link request expired. Start the connection again.";
    case "provider_error":
      return "kombify Cloud login was cancelled or failed.";
    case "cloud_oidc_not_configured":
      return "This instance has no kombify Cloud login configured.";
    default:
      return "Linking the kombify Cloud profile failed. Try again.";
  }
}
