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
import type {
  LayoutFlow,
  LayoutPlannedNode,
  LayoutRecommendationAcceptance,
  LayoutSelection,
} from "#lib/api/layout.js";
import {
  isValidSubstrateGuest,
  readRuntimeOfferingID,
  type StackConfig,
} from "./types";
import {
  selectedCanonicalUseCasesFromGoals,
  normalizeInstallableKitSlug,
} from "./standardBundle";

import { tr } from "#lib/i18n.svelte.js";
import { remoteSshKeyLabel } from "./remote-credential";
/**
 * An answer that cannot become a run. Its message is already written for the
 * person (catalog text), unlike an unexpected error, so the creation screen
 * shows it as the explanation instead of hiding it in technical details.
 */
export class WizardInputError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "WizardInputError";
  }
}

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

/** Capability selections for selected goals only, mirroring runUseCaseSettings. */
function runUseCaseCapabilities(
  config: StackConfig,
  goals: string[],
): WizardIntent["use_case_capabilities"] {
  const all = config.useCaseCapabilities ?? {};
  const out: Record<string, Record<string, string>> = {};
  for (const slug of goals) {
    const values = all[slug];
    if (!values || Object.keys(values).length === 0) continue;
    out[slug] = { ...values };
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

/** Preserve desired decisions separately from release-admitted execution. */
function runUseCaseSetupAnswers(
  config: StackConfig,
  goals: string[],
): WizardIntent["use_case_setup_answers"] {
  const selected = Object.entries(config.useCaseSetupAnswers ?? {}).filter(
    ([slug]) => goals.includes(slug),
  );
  return selected.length
    ? Object.fromEntries(
        selected.map(([slug, answers]) => [
          slug,
          Object.fromEntries(
            Object.entries(answers).map(([id, choices]) => [id, [...choices]]),
          ),
        ]),
      )
    : undefined;
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
  managed.runtime_offering_id = readRuntimeOfferingID(config.runtimeOfferingId);
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
  if (remote.useSudo) params.use_sudo = true;
  const walletItem = remote.sshWalletItemId?.trim() ?? "";
  if (walletItem !== "") {
    // The credential lives in the owner's Wallet item, which has to hold the
    // secret of the chosen authentication method; no label or password leaves
    // the browser.
    if (remote.authMethod) params.auth_method = remote.authMethod;
    params.ssh_wallet_item_id = walletItem;
    return params;
  }
  if (remote.authMethod) params.auth_method = remote.authMethod;
  const keyLabel = remoteSshKeyLabel(remote);
  if (keyLabel) params.ssh_key_label = keyLabel;
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
  // The server refuses this too (wizard_accelerator_required); saying it here
  // keeps the reason in the person's words instead of a failed run.
  if (guest?.requiresAccelerator && !guest.accelerator?.address)
    throw new WizardInputError(tr("wizard.server.hypervisor.gpuNeeded"));
  if (!isValidSubstrateGuest(guest) || !guest)
    throw new WizardInputError(
      tr("ui.wizardRunRequest.selectAConnectedHypervisorAnd"),
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
    throw new WizardInputError(
      tr("ui.wizardRunRequest.selectSeparateHomeAssistantOS"),
    );
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
    ...(guest.requiresAccelerator && guest.accelerator
      ? { accelerator: { ...guest.accelerator } }
      : {}),
  };
}

/**
 * A found run for /stacks/new: the first run founds the homelab (the backend
 * coerces to an expansion when one already operates deployments). The pinned
 * release authors the service graph from goals, settings and capabilities.
 */
export function buildFoundRunRequest(config: StackConfig): WizardRunRequest {
  if (config.network.publicAccess) {
    throw new WizardInputError(tr("wizard.users.public.pending"));
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
      ...(config.network.publications
        ? {
            publications: config.network.publications.map((publication) => ({
              ...publication,
            })),
          }
        : {}),
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
  intent.use_case_setup_answers = runUseCaseSetupAnswers(
    config,
    intent.goals ?? [],
  );
  const foundSettings = runUseCaseSettings(config, intent.goals ?? []);
  if (foundSettings) intent.use_case_settings = foundSettings;
  const foundCapabilities = runUseCaseCapabilities(config, intent.goals ?? []);
  if (foundCapabilities) intent.use_case_capabilities = foundCapabilities;
  const request: WizardRunRequest = { intent };
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
  layoutRecommendation?: LayoutRecommendationAcceptance,
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
  intent.use_case_setup_answers = runUseCaseSetupAnswers(
    config,
    intent.goals ?? [],
  );
  const joinSettings = runUseCaseSettings(config, intent.goals ?? []);
  if (joinSettings) intent.use_case_settings = joinSettings;
  const joinCapabilities = runUseCaseCapabilities(config, intent.goals ?? []);
  if (joinCapabilities) intent.use_case_capabilities = joinCapabilities;
  applyLayoutAcceptance(intent, layoutRecommendation);
  const request: WizardRunRequest = { intent, services };
  const substrate = buildRunSubstrateParams(config);
  if (substrate) request.substrate = substrate;
  const managed = buildRunManagedParams(config);
  if (managed) request.managed = managed;
  const remote = buildRunRemoteParams(config);
  if (remote) request.remote = remote;
  return request;
}

/**
 * An extend run for /stacks/[id]/use-cases/new: add use cases to the existing
 * deployment without a new server. The deployment's own nodes run them, as the
 * accepted recommendation distributes them. Goals are required (the backend
 * refuses an extend that adds nothing) and no server section is needed.
 */
export function buildExtendRunRequest(
  config: StackConfig,
  deploymentId: string,
  deploymentName: string,
  layoutRecommendation?: LayoutRecommendationAcceptance,
): WizardRunRequest {
  const goals = selectedCanonicalUseCasesFromGoals(config.goals);
  if (goals.length === 0) {
    throw new WizardInputError(tr("wizard.validation.goalsRequired"));
  }
  const intent: WizardIntent = {
    schema: WIZARD_INTENT_SCHEMA,
    run_kind: "expansion",
    name: (deploymentName || "").trim() || DEFAULT_RUN_NAME,
    goals,
    server: {},
    kit_assignment: { mode: "extend", kit_deployment_id: deploymentId },
  };
  intent.use_case_setup_answers = runUseCaseSetupAnswers(config, goals);
  const settings = runUseCaseSettings(config, goals);
  if (settings) intent.use_case_settings = settings;
  const capabilities = runUseCaseCapabilities(config, goals);
  if (capabilities) intent.use_case_capabilities = capabilities;
  applyLayoutAcceptance(intent, layoutRecommendation);
  return { intent };
}

/** The recommendation a run accepts; absent unless a verified one was shown. */
function applyLayoutAcceptance(
  intent: WizardIntent,
  acceptance: LayoutRecommendationAcceptance | undefined,
): void {
  if (!acceptance?.id || !acceptance.proposal_digest) return;
  intent.layout_recommendation = {
    id: acceptance.id,
    proposal_digest: acceptance.proposal_digest,
  };
}

/** The server a hypervisor guest will become, sized as the owner configured it. */
function plannedGuestNode(config: StackConfig): LayoutPlannedNode | undefined {
  if (config.serverProvisioning.mode !== "hypervisor") return undefined;
  const guest = config.serverProvisioning.substrate;
  if (!guest || !isValidSubstrateGuest(guest)) return undefined;
  const server = joinServer(config);
  return {
    name: tr("layout.panel.newServer"),
    cpu_cores: guest.cpu,
    ram_gb: guest.memoryMiB / 1024,
    storage_gb: guest.diskGiB,
    source: "guest-sizing",
    ...(server.roles ? { roles: server.roles } : {}),
    ...(server.transport ? { transport: server.transport } : {}),
  };
}

/**
 * The question for the layout recommendation: the selection the run would
 * send, or null while nothing is selected (nothing to distribute). An
 * add-server run with a hypervisor guest names the guest's sizing as the
 * planned node; every other server is distributed as it enrolls.
 */
export function buildLayoutSelection(
  config: StackConfig,
  stackId: string,
  flow: LayoutFlow,
): LayoutSelection | null {
  const goals = selectedCanonicalUseCasesFromGoals(config.goals);
  const id = stackId.trim();
  if (!id || goals.length === 0) return null;
  const selection: LayoutSelection = { flow, stack_id: id, goals };
  const settings = runUseCaseSettings(config, goals);
  if (settings) selection.use_case_settings = settings;
  const capabilities = runUseCaseCapabilities(config, goals);
  if (capabilities) selection.use_case_capabilities = capabilities;
  if (flow === "add-server") {
    const planned = plannedGuestNode(config);
    if (planned) selection.planned_node = planned;
  }
  return selection;
}
