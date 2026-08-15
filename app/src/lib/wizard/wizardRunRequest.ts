/**
 * StackConfig -> wizardRunRequest projection for the native v2 wizard
 * (POST /api/v1/wizard/runs). Pure mapping, no state: the wizard's collected
 * answers become the backend's closed intent contract.
 *
 * The owner section must use the backend option spellings (snake_case) — the
 * facade copies it verbatim into create-core options where
 * applyOwnerBootstrapFromOptions reads exactly owner_bootstrap_mode,
 * owner_source, owner_email, owner_username, owner_display_name, and
 * recovery_passphrase_hash. For the cloud-linked source the identity fields
 * must be omitted entirely (the backend rejects them fail-closed).
 */
import {
  WIZARD_INTENT_SCHEMA,
  type WizardIntent,
  type WizardRunManagedParams,
  type WizardRunRemoteParams,
  type WizardRunRequest,
} from "$lib/api/wizardRuns";
import type { StackConfig } from "./types";
import {
  buildPayloadServicesFromBundle,
  selectedCanonicalUseCasesFromGoals,
  CLOUD_STACKKIT_REF,
  LEGACY_BASE_STACKKIT_REF,
  USER_OWNED_STACKKIT_REF,
} from "./standardBundle";

/** The wizard's fallback deployment name (EasyWizard has no name input). */
const DEFAULT_RUN_NAME = "homelab";

/**
 * Normalize the client kit reference onto an installable v2 kit slug. The
 * legacy "base-kit" value is still representable in persisted wizard state
 * but would 400 against the closed intent contract.
 */
export function normalizeRunKitSlug(config: StackConfig): string {
  const raw = (
    config.serverProvisioning.stackkitFoundation ||
    config.kit ||
    ""
  ).trim();
  if (raw === "" || raw === LEGACY_BASE_STACKKIT_REF) {
    return config.serverProvisioning.mode === "kombify-cloud"
      ? CLOUD_STACKKIT_REF
      : USER_OWNED_STACKKIT_REF;
  }
  return raw;
}

function runTransport(
  config: StackConfig,
): WizardIntent["server"]["transport"] {
  switch (config.serverProvisioning.mode) {
    case "connect-remote":
      return "connect-remote";
    case "kombify-cloud":
      return "kombify-cloud";
    default:
      return "install-command";
  }
}

function runServer(config: StackConfig): WizardIntent["server"] {
  const server: WizardIntent["server"] = {
    transport: runTransport(config),
  };
  const role = (config.serverProvisioning.nodeRole || "").trim();
  if (role !== "") {
    server.roles = [role];
  }
  return server;
}

/**
 * Owner-bootstrap options in the backend spelling. Returns undefined when the
 * run carries no bootstrap (mode "none" — e.g. add-server owner reuse).
 */
export function buildRunOwnerOptions(
  config: StackConfig,
): Record<string, unknown> | undefined {
  const owner = config.owner;
  if (!owner || owner.bootstrapMode === "none") {
    return undefined;
  }
  const options: Record<string, unknown> = {
    owner_bootstrap_mode: owner.bootstrapMode,
    owner_source: owner.source,
  };
  // cloud-linked derives identity solely from the verified cloud link; any
  // client-supplied identity field is rejected fail-closed by the backend.
  if (owner.source !== "cloud-linked") {
    if (owner.email.trim() !== "") options.owner_email = owner.email.trim();
    if (owner.username.trim() !== "")
      options.owner_username = owner.username.trim();
    if (owner.displayName.trim() !== "")
      options.owner_display_name = owner.displayName.trim();
  }
  if (owner.recoveryPassphraseHash.trim() !== "") {
    options.recovery_passphrase_hash = owner.recoveryPassphraseHash.trim();
  }
  return options;
}

function buildRunManagedParams(
  config: StackConfig,
): WizardRunManagedParams | undefined {
  if (config.serverProvisioning.mode !== "kombify-cloud") {
    return undefined;
  }
  const managed: WizardRunManagedParams = {
    provider_id: config.providerId || "",
  };
  if (config.runtimeOfferingId) {
    managed.runtime_offering_id = config.runtimeOfferingId;
  }
  if (config.providerId === "ionos" && config.ionosDatacenter) {
    managed.ionos_datacenter = config.ionosDatacenter;
    managed.provider_region = config.ionosDatacenter;
  }
  return managed;
}

function buildRunRemoteParams(
  config: StackConfig,
): WizardRunRemoteParams | undefined {
  if (config.serverProvisioning.mode !== "connect-remote") {
    return undefined;
  }
  const remote = config.serverProvisioning.remote;
  const params: WizardRunRemoteParams = {};
  if (remote.host.trim() !== "") params.host = remote.host.trim();
  if (remote.sshPort > 0) params.port = remote.sshPort;
  if (remote.sshUser.trim() !== "") params.user = remote.sshUser.trim();
  if (remote.authMethod) params.auth_method = remote.authMethod;
  if (remote.sshKeyLabel.trim() !== "")
    params.ssh_key_label = remote.sshKeyLabel.trim();
  if (remote.useSudo) params.use_sudo = true;
  return params;
}

/**
 * A found run for /stacks/new: the first run founds the homelab (the backend
 * coerces to an expansion when one already operates deployments). Services
 * use the payload wire names — the same vocabulary the legacy create request
 * sent for first runs.
 */
export function buildFoundRunRequest(config: StackConfig): WizardRunRequest {
  const intent: WizardIntent = {
    schema: WIZARD_INTENT_SCHEMA,
    run_kind: "first-run",
    name: (config.name || "").trim() || DEFAULT_RUN_NAME,
    goals: selectedCanonicalUseCasesFromGoals(config.goals),
    server: runServer(config),
    kit_assignment: {
      mode: "found",
      kit_slug: normalizeRunKitSlug(config),
    },
  };
  const request: WizardRunRequest = {
    intent,
    services: buildPayloadServicesFromBundle(config.services),
  };
  const owner = buildRunOwnerOptions(config);
  if (owner) request.owner = owner;
  const managed = buildRunManagedParams(config);
  if (managed) request.managed = managed;
  const remote = buildRunRemoteParams(config);
  if (remote) request.remote = remote;
  return request;
}

/**
 * A join run for /stacks/[id]/servers/new: append this server to the existing
 * kit deployment. Never carries an owner section (the backend ignores it on
 * expansions, but sending it would make the idempotency fingerprint depend on
 * blanked owner state). Services use the StackConfig keys — the same
 * vocabulary the legacy pairing request sent; the backend normalizes both.
 */
export function buildJoinRunRequest(
  config: StackConfig,
  deploymentId: string,
  deploymentName: string,
  services: string[],
): WizardRunRequest {
  const intent: WizardIntent = {
    schema: WIZARD_INTENT_SCHEMA,
    run_kind: "expansion",
    name: (deploymentName || "").trim() || DEFAULT_RUN_NAME,
    goals: selectedCanonicalUseCasesFromGoals(config.goals),
    server: runServer(config),
    kit_assignment: {
      mode: "join",
      kit_deployment_id: deploymentId,
    },
  };
  const request: WizardRunRequest = { intent, services };
  const remote = buildRunRemoteParams(config);
  if (remote) request.remote = remote;
  return request;
}
