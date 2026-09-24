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
  type WizardRunSubstrateParams,
  type WizardRunRequest,
} from "#lib/api/wizardRuns.js";
import { isValidSubstrateGuest, type StackConfig } from "./types";
import {
  buildPayloadServicesFromBundle,
  selectedCanonicalUseCasesFromGoals,
  normalizeInstallableKitSlug,
} from "./standardBundle";

/** The wizard's fallback deployment name (EasyWizard has no name input). */
const DEFAULT_RUN_NAME = "homelab";

/**
 * The decisions to send: only for use cases in `goals`, only settings that
 * were set explicitly. The backend validates every id and value against the
 * catalog, so nothing is normalised here.
 */
function runUseCaseSettings(
  config: StackConfig,
  goals: string[],
): WizardIntent["use_case_settings"] {
  const all = config.useCaseSettings ?? {};
  const out: Record<string, Record<string, string | boolean>> = {};
  for (const slug of goals) {
    const values = all[slug];
    if (!values || Object.keys(values).length === 0) continue;
    out[slug] = { ...values };
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

/** Normalize the client kit reference onto an installable v2 kit slug. */
export function normalizeRunKitSlug(config: StackConfig): string {
  return normalizeInstallableKitSlug(
    config.serverProvisioning.stackkitFoundation || config.kit || "",
    config.serverProvisioning.mode === "kombify-cloud",
  );
}

/**
 * Whether a run request carries a credential that must never reach browser
 * storage. The add-Node handoff keeps the request in memory only when this is
 * true; an SSH password is never written to sessionStorage and is re-entered
 * after a reload (the server wallet takes over custody once the run exists).
 */
export function wizardRunRequestCarriesSecret(
  request: WizardRunRequest,
): boolean {
  return (request.remote?.password ?? "") !== "";
}

function runTransport(
  config: StackConfig,
): WizardIntent["server"]["transport"] {
  switch (config.serverProvisioning.mode) {
    case "hypervisor":
      return "hypervisor";
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

function joinServer(config: StackConfig): WizardIntent["server"] {
  const server = runServer(config);
  const role = (server.roles?.[0] || "").trim().toLowerCase();
  if (
    role === "" ||
    role === "foundation" ||
    role === "controller" ||
    role === "standalone" ||
    role === "main" ||
    role === "control-plane"
  ) {
    server.roles = ["worker"];
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
  // Profile identities derive solely from the verified server-side session
  // or link. Stale local drafts must never become authority for either path.
  if (owner.bootstrapMode === "custom" && owner.source === "local") {
    if (owner.email.trim() !== "") options.owner_email = owner.email.trim();
    if (owner.username.trim() !== "")
      options.owner_username = owner.username.trim();
    if (owner.displayName.trim() !== "")
      options.owner_display_name = owner.displayName.trim();
  }
  if (
    owner.bootstrapMode === "custom" &&
    owner.recoveryPassphraseHash.trim() !== ""
  ) {
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
  if (remote.authMethod === "password" && remote.sshPassword.trim() !== "") {
    params.password = remote.sshPassword;
  }
  return params;
}

function buildRunSubstrateParams(
  config: StackConfig,
): WizardRunSubstrateParams | undefined {
  if (config.serverProvisioning.mode !== "hypervisor") return undefined;
  const guest = config.serverProvisioning.substrate;
  if (!isValidSubstrateGuest(guest) || !guest)
    throw new Error(
      "Select a connected hypervisor and valid Ubuntu guest resources.",
    );
  const wantsAppliance =
    config.goals?.["smart-home"] &&
    config.useCaseSettings?.["smart-home"]?.["operating-form"] === "haos" &&
    config.useCaseSettings?.["smart-home"]?.["instance-origin"] !== "existing";
  const appliance = wantsAppliance ? guest.appliance : undefined;
  if (
    wantsAppliance &&
    (!appliance ||
      !appliance.storage ||
      !appliance.bridge ||
      !Number.isInteger(appliance.cpu) ||
      !Number.isInteger(appliance.memoryMiB) ||
      !Number.isInteger(appliance.diskGiB))
  )
    throw new Error("Select separate Home Assistant OS guest resources.");
  return {
    ...(appliance
      ? {
          appliance: {
            profile_id: appliance.profileId,
            storage: appliance.storage,
            bridge: appliance.bridge,
            cpu: appliance.cpu,
            memory_mib: appliance.memoryMiB,
            disk_gib: appliance.diskGiB,
          },
        }
      : {}),
    server_id: guest.serverId,
    profile_id: guest.profileId,
    storage: guest.storage,
    bridge: guest.bridge,
    cpu: guest.cpu,
    memory_mib: guest.memoryMiB,
    disk_gib: guest.diskGiB,
  };
}

/**
 * A found run for /stacks/new: the first run founds the homelab (the backend
 * coerces to an expansion when one already operates deployments). Services
 * use the payload wire names — the same vocabulary the legacy create request
 * sent for first runs.
 */
export function buildFoundRunRequest(config: StackConfig): WizardRunRequest {
  if (config.network.publicAccess) {
    throw new Error("wizard.users.public.pending");
  }
  const intent: WizardIntent = {
    schema: WIZARD_INTENT_SCHEMA,
    run_kind: "first-run",
    name: (config.name || "").trim() || DEFAULT_RUN_NAME,
    ...(config.network.domainBase?.trim()
      ? { domain_base: config.network.domainBase.trim().toLowerCase() }
      : {}),
    access: {
      mode: config.network.accessMode === "home" ? "local" : "remote-private",
    },
    household: {
      profile:
        config.household?.profile ??
        (config.audience.onlyMe ? "solo" : "shared"),
      planned_people:
        config.household?.profile === "solo"
          ? undefined
          : config.household?.people
              .filter((person) => person.name.trim() || person.email.trim())
              .map((person) => ({
                client_ref: person.id,
                name: person.name.trim() || undefined,
                email: person.email.trim() || undefined,
              })),
    },
    goals: selectedCanonicalUseCasesFromGoals(config.goals),
    server: runServer(config),
    kit_assignment: {
      mode: "found",
      kit_slug: normalizeRunKitSlug(config),
    },
  };
  const foundSettings = runUseCaseSettings(config, intent.goals ?? []);
  if (foundSettings) intent.use_case_settings = foundSettings;
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
  const substrate = buildRunSubstrateParams(config);
  if (substrate) request.substrate = substrate;
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
    // Use cases the operator picked for this Node. The backend folds an
    // expansion's goals into the homelab's durable backlog rather than
    // replacing it, so this is additive by construction. It is only safe
    // because the Additional Node config starts with an empty selection —
    // the first-run default (vault) used to ride along here and fail CLI
    // validate as a "service conflict", which is why this was once [].
    goals: selectedCanonicalUseCasesFromGoals(config.goals),
    server: joinServer(config),
    kit_assignment: {
      mode: "join",
      kit_deployment_id: deploymentId,
      // A historical deployment may not have a canonical v2 spec yet. Carry
      // the operator's selected kit so migrate-on-write never has to infer it
      // from incomplete legacy metadata.
      kit_slug: normalizeRunKitSlug(config),
    },
  };
  const joinSettings = runUseCaseSettings(config, intent.goals ?? []);
  if (joinSettings) intent.use_case_settings = joinSettings;
  const request: WizardRunRequest = { intent, services };
  const substrate = buildRunSubstrateParams(config);
  if (substrate) request.substrate = substrate;
  const managed = buildRunManagedParams(config);
  if (managed) request.managed = managed;
  const remote = buildRunRemoteParams(config);
  if (remote) request.remote = remote;
  return request;
}
