import { tr } from "#lib/i18n.svelte.js";
/**
 * kombify-TechStack Initialization Tasks
 *
 * Task definitions and per-phase status aggregation for the stack creation
 * progress view.
 *
 * IMPORTANT: These are UNIFIER tasks - they run BEFORE any server is known!
 * The Unifier transforms user input into a standardized StackKits stack-spec.
 * Infrastructure provisioning happens LATER, after the user sets up their servers.
 */

export interface Task {
  id: string;
  label: string;
  labelKey?: string; // i18n key
  status: "pending" | "running" | "completed" | "failed";
  message?: string;
  progress?: number; // 0-100
  errorMessage?: string; // Short error description
  errorDetails?: string; // Detailed error info for troubleshooting
  troubleshooting?: string[]; // Actionable troubleshooting steps
}

/**
 * Unifier Pipeline Tasks - these run BEFORE any server exists
 * The goal is to transform user goals into a valid StackKits stack-spec.
 */
export const DEFAULT_TASKS: Omit<Task, "status">[] = [
  {
    id: "validate",
    get label() {
      return tr("ui.taskStatus.validatingConfiguration");
    },
    labelKey: "tasks.validate",
  },
  {
    id: "save_config",
    get label() {
      return tr("ui.taskStatus.savingYourConfiguration");
    },
    labelKey: "tasks.save_config",
  },
  {
    id: "find_stackkit",
    get label() {
      return tr("ui.taskStatus.findingTheBestStackKitFor");
    },
    labelKey: "tasks.find_stackkit",
  },
  {
    id: "unify_services",
    get label() {
      return tr("ui.taskStatus.identifyingServicesBestPractices");
    },
    labelKey: "tasks.unify_services",
  },
  {
    id: "unify_network",
    get label() {
      return tr("ui.taskStatus.configuringNetworkSettings");
    },
    labelKey: "tasks.unify_network",
  },
  {
    id: "unify_security",
    get label() {
      return tr("ui.taskStatus.settingUpSecurityConfiguration");
    },
    labelKey: "tasks.unify_security",
  },
  {
    id: "unify_auth",
    get label() {
      return tr("ui.taskStatus.configuringAuthentication");
    },
    labelKey: "tasks.unify_auth",
  },
  {
    id: "create_spec",
    get label() {
      return tr("ui.taskStatus.creatingYourDeploymentSpec");
    },
    labelKey: "tasks.create_spec",
  },
];

export const RUNTIME_TASKS: Omit<Task, "status">[] = [
  {
    id: "create_lease",
    get label() {
      return tr("ui.taskStatus.requestingManagedCloudNode");
    },
    labelKey: "tasks.runtime.create_lease",
  },
  {
    id: "prepare_rollout",
    get label() {
      return tr("ui.taskStatus.confirmingVPSTarget");
    },
    labelKey: "tasks.runtime.prepare_rollout",
  },
  {
    id: "runtime_connected",
    get label() {
      return tr("ui.taskStatus.connectingToRuntime");
    },
    labelKey: "tasks.runtime.runtime_connected",
  },
  {
    id: "telemetry_handshake",
    get label() {
      return tr("ui.taskStatus.startingTelemetryHandoff");
    },
    labelKey: "tasks.runtime.telemetry_handshake",
  },
  {
    id: "validate_workers",
    get label() {
      return tr("ui.taskStatus.checkingRolloutTarget");
    },
    labelKey: "tasks.runtime.validate_workers",
  },
  {
    id: "generate_unified",
    get label() {
      return tr("ui.taskStatus.generatingUnifiedSpec");
    },
    labelKey: "tasks.runtime.generate_unified",
  },
  {
    id: "persist_unified",
    get label() {
      return tr("ui.taskStatus.persistingRolloutSpec");
    },
    labelKey: "tasks.runtime.persist_unified",
  },
  {
    id: "generate_iac",
    get label() {
      return tr("ui.taskStatus.generatingStackKitIaC");
    },
    labelKey: "tasks.runtime.generate_iac",
  },
  {
    id: "simulate_update",
    get label() {
      return tr("ui.taskStatus.runningSimulatedUpdateGate");
    },
    labelKey: "tasks.runtime.simulate_update",
  },
  {
    id: "stackkit_prepare",
    get label() {
      return tr("ui.taskStatus.preparingStackKitsRuntime");
    },
    labelKey: "tasks.runtime.stackkit_prepare",
  },
  {
    id: "docker_ready",
    get label() {
      return tr("ui.taskStatus.installingAndCheckingDocker");
    },
    labelKey: "tasks.runtime.docker_ready",
  },
  {
    id: "opentofu_ready",
    get label() {
      return tr("ui.taskStatus.checkingOpenTofu");
    },
    labelKey: "tasks.runtime.opentofu_ready",
  },
  {
    id: "terramate_ready",
    get label() {
      return tr("ui.taskStatus.checkingTerramate");
    },
    labelKey: "tasks.runtime.terramate_ready",
  },
  {
    id: "telemetry_ready",
    get label() {
      return tr("ui.taskStatus.preparingTelemetry");
    },
    labelKey: "tasks.runtime.telemetry_ready",
  },
  {
    id: "stackkit_rollout",
    get label() {
      return tr("ui.taskStatus.rollingOutCloudKit");
    },
    labelKey: "tasks.runtime.stackkit_rollout",
  },
  {
    id: "service_inventory",
    get label() {
      return tr("ui.taskStatus.readingServiceInventory");
    },
    labelKey: "tasks.runtime.service_inventory",
  },
  {
    id: "verify_rollout",
    get label() {
      return tr("ui.taskStatus.verifyingLoginProtectedServices");
    },
    labelKey: "tasks.runtime.verify_rollout",
  },
  {
    id: "restore_drill",
    get label() {
      return tr("ui.taskStatus.runningRestoreDrill");
    },
    labelKey: "tasks.runtime.restore_drill",
  },
];

export const ADD_SERVER_MANAGED_RUNTIME_TASKS: Omit<Task, "status">[] = [
  {
    id: "create_lease",
    get label() {
      return tr("ui.taskStatus.requestingManagedNode");
    },
    labelKey: "tasks.runtime.create_lease",
  },
];

export const ADD_SERVER_REGISTRATION_TASKS: Omit<Task, "status">[] = [
  {
    id: "create_spec",
    get label() {
      return tr("ui.taskStatus.preparingNodeRegistration");
    },
    labelKey: "tasks.create_spec",
  },
];

/**
 * Step detail descriptions shown on the right side during creation.
 * Each step explains what is happening and why.
 */
export const STEP_DETAILS: Record<
  string,
  { title: string; description: string; detail: string }
> = {
  validate: {
    get title() {
      return tr("ui.taskStatus.checkingYourChoices");
    },
    get description() {
      return tr("ui.taskStatus.verifyingThatAllSelectedOptions");
    },
    get detail() {
      return tr("ui.taskStatus.thisChecksFeatureSelectionsAccess");
    },
  },
  save_config: {
    get title() {
      return tr("ui.taskStatus.persistingConfiguration");
    },
    get description() {
      return tr("ui.taskStatus.savingYourChoicesToThe");
    },
    get detail() {
      return tr("ui.taskStatus.thisStackKitDeploymentConfigurationIs");
    },
  },
  find_stackkit: {
    get title() {
      return tr("ui.taskStatus.matchingAStackKit");
    },
    get description() {
      return tr("ui.taskStatus.analyzingYourGoalsToFind");
    },
    get detail() {
      return tr("ui.taskStatus.stackkitsAreCuratedInfrastructureTemplates");
    },
  },
  unify_services: {
    get title() {
      return tr("ui.taskStatus.buildingServiceList");
    },
    get description() {
      return tr("ui.taskStatus.determiningWhichServicesAreNeeded");
    },
    get detail() {
      return tr("ui.taskStatus.basedOnYourGoalsThe");
    },
  },
  unify_network: {
    get title() {
      return tr("ui.taskStatus.settingUpNetworking");
    },
    get description() {
      return tr("ui.taskStatus.configuringAccessProfilesReverseProxy");
    },
    get detail() {
      return tr("ui.taskStatus.networkSettingsAreDerivedFrom");
    },
  },
  unify_security: {
    get title() {
      return tr("ui.taskStatus.applyingSecurityPolicies");
    },
    get description() {
      return tr("ui.taskStatus.configuringFirewallRulesTLSCertificates");
    },
    get detail() {
      return tr("ui.taskStatus.eachServiceGetsScopedPermissions");
    },
  },
  unify_auth: {
    get title() {
      return tr("ui.taskStatus.settingUpAuthentication");
    },
    get description() {
      return tr("ui.taskStatus.configuringSingleSignOnUser");
    },
    get detail() {
      return tr("ui.taskStatus.yourChosenAuthMethodIs");
    },
  },
  create_spec: {
    get title() {
      return tr("ui.taskStatus.generatingDeploymentSpec");
    },
    get description() {
      return tr("ui.taskStatus.compilingEverythingIntoAFinal");
    },
    get detail() {
      return tr("ui.taskStatus.theSpecContainsAllConfiguration");
    },
  },
  create_lease: {
    get title() {
      return tr("ui.taskStatus.requestingManagedNode");
    },
    get description() {
      return tr("ui.taskStatus.creatingOrBindingTheSubscription");
    },
    get detail() {
      return tr("ui.taskStatus.theLeaseCapturesRuntimeState");
    },
  },
  prepare_rollout: {
    get title() {
      return tr("ui.taskStatus.confirmingVPSTarget");
    },
    get description() {
      return tr("ui.taskStatus.loadingThePersistedIntentAnd");
    },
    get detail() {
      return tr("ui.taskStatus.thisChecksThatThePersisted");
    },
  },
  runtime_connected: {
    get title() {
      return tr("ui.taskStatus.connectingToRuntime");
    },
    get description() {
      return tr("ui.taskStatus.confirmingThatTheBoundManaged");
    },
    get detail() {
      return tr("ui.taskStatus.onceThisSucceedsTheNode");
    },
  },
  telemetry_handshake: {
    get title() {
      return tr("ui.taskStatus.startingTelemetryHandoff");
    },
    get description() {
      return tr("ui.taskStatus.preparingTheRuntimeMetadataUsed");
    },
    get detail() {
      return tr("ui.taskStatus.techstackRecordsTheManagedTarget");
    },
  },
  validate_workers: {
    get title() {
      return tr("ui.taskStatus.checkingRolloutTarget");
    },
    get description() {
      return tr("ui.taskStatus.ensuringTheDeploymentTargetSatisfies");
    },
    get detail() {
      return tr("ui.taskStatus.managedKombifyCloudRolloutsUse");
    },
  },
  generate_unified: {
    get title() {
      return tr("ui.taskStatus.generatingUnifiedSpec");
    },
    get description() {
      return tr("ui.taskStatus.combiningUserIntentStackKitDefaults");
    },
    get detail() {
      return tr("ui.taskStatus.theUnifiedSpecIsThe");
    },
  },
  persist_unified: {
    get title() {
      return tr("ui.taskStatus.persistingRolloutSpec");
    },
    get description() {
      return tr("ui.taskStatus.savingUnifiedSpecYamlSo");
    },
    get detail() {
      return tr("ui.taskStatus.thePersistedSpecLinksBack");
    },
  },
  generate_iac: {
    get title() {
      return tr("ui.taskStatus.generatingStackKitIaC");
    },
    get description() {
      return tr("ui.taskStatus.renderingTheStackKitInfrastructureFiles");
    },
    get detail() {
      return tr("ui.taskStatus.techstackConsumesStackKitArtifactsHere");
    },
  },
  simulate_update: {
    get title() {
      return tr("ui.taskStatus.runningSimulationGate");
    },
    get description() {
      return tr("ui.taskStatus.validatingTheUpdatePathBefore");
    },
    get detail() {
      return tr("ui.taskStatus.theSimulationGateProtectsThe");
    },
  },
  stackkit_prepare: {
    get title() {
      return tr("ui.taskStatus.preparingStackKitsRuntime");
    },
    get description() {
      return tr("ui.taskStatus.runningTheStackKitsCLIPrepare");
    },
    get detail() {
      return tr("ui.taskStatus.thisNonInteractivePrepStep");
    },
  },
  docker_ready: {
    get title() {
      return tr("ui.taskStatus.preparingDocker");
    },
    get description() {
      return tr("ui.taskStatus.installingOrValidatingTheDocker");
    },
    get detail() {
      return tr("ui.taskStatus.ifAptOrUnattendedUpgrades");
    },
  },
  opentofu_ready: {
    get title() {
      return tr("ui.taskStatus.checkingOpenTofu");
    },
    get description() {
      return tr("ui.taskStatus.verifyingTheInfrastructureToolchainNeeded");
    },
    get detail() {
      return tr("ui.taskStatus.opentofuReadinessIsPartOf");
    },
  },
  terramate_ready: {
    get title() {
      return tr("ui.taskStatus.checkingTerramate");
    },
    get description() {
      return tr("ui.taskStatus.checkingTheTerramateToolchainWhen");
    },
    get detail() {
      return tr("ui.taskStatus.terramateReadinessBelongsToStackKits");
    },
  },
  telemetry_ready: {
    get title() {
      return tr("ui.taskStatus.preparingTelemetry");
    },
    get description() {
      return tr("ui.taskStatus.preparingOpenTelemetryHandoffDataFor");
    },
    get detail() {
      return tr("ui.taskStatus.theFirstRolloutRecordsThe");
    },
  },
  stackkit_rollout: {
    get title() {
      return tr("ui.taskStatus.rollingOutCloudKit");
    },
    get description() {
      return tr("ui.taskStatus.callingTheStackKitsRuntimeAction");
    },
    get detail() {
      return tr("ui.taskStatus.thisIsThePointWhere");
    },
  },
  service_inventory: {
    get title() {
      return tr("ui.taskStatus.readingServiceInventory");
    },
    get description() {
      return tr("ui.taskStatus.collectingServiceMetadataExposedBy");
    },
    get detail() {
      return tr("ui.taskStatus.theDashboardCanShowManaged");
    },
  },
  verify_rollout: {
    get title() {
      return tr("ui.taskStatus.verifyingServices");
    },
    get description() {
      return tr("ui.taskStatus.checkingThatLoginProtectedServices");
    },
    get detail() {
      return tr("ui.taskStatus.verificationConfirmsThatTheStackKit");
    },
  },
  restore_drill: {
    get title() {
      return tr("ui.taskStatus.runningRestoreDrill");
    },
    get description() {
      return tr("ui.taskStatus.validatingTheBackupAndRestore");
    },
    get detail() {
      return tr("ui.taskStatus.aVerifiedStackKitDeploymentMust");
    },
  },
};

/**
 * Phase groups that consolidate the long step list into a few main work
 * packages. The flat task list runs 18 steps for the kombify-cloud rollout,
 * but the first ~8 (Unifier pipeline) finish in milliseconds and drown out
 * the slower runtime work. Phases let the UI show one card per work package
 * with a drill-down for the underlying step IDs.
 *
 * The taskIds must match the ids in DEFAULT_TASKS / RUNTIME_TASKS and the
 * backend step constants in pkg/jobs/handlers.go.
 */
export interface TaskGroup {
  id: string;
  label: string;
  description: string;
  taskIds: string[];
  visibleTaskIds?: string[];
}

export const TASK_GROUPS: TaskGroup[] = [
  {
    id: "configure",
    get label() {
      return tr("ui.taskStatus.configureStackKitDeployment");
    },
    get description() {
      return tr("ui.taskStatus.validatingChoicesAndBuildingThe");
    },
    taskIds: [
      "validate",
      "save_config",
      "find_stackkit",
      "unify_services",
      "unify_network",
      "unify_security",
      "unify_auth",
      "create_spec",
    ],
    visibleTaskIds: ["validate", "find_stackkit", "create_spec"],
  },
  {
    id: "provision",
    get label() {
      return tr("ui.taskStatus.provisionManagedRuntime");
    },
    get description() {
      return tr("ui.taskStatus.reservingTheSubscriptionVMConnecting");
    },
    taskIds: [
      "create_lease",
      "prepare_rollout",
      "runtime_connected",
      "telemetry_handshake",
      "validate_workers",
    ],
    visibleTaskIds: [
      "create_lease",
      "prepare_rollout",
      "runtime_connected",
      "telemetry_handshake",
    ],
  },
  {
    id: "generate",
    get label() {
      return tr("ui.taskStatus.generateDeploymentArtifacts");
    },
    get description() {
      return tr("ui.taskStatus.renderingStackKitsArtifactsAfterVPS");
    },
    taskIds: ["generate_unified", "persist_unified", "generate_iac"],
    visibleTaskIds: ["generate_unified", "generate_iac"],
  },
  {
    id: "rollout",
    get label() {
      return tr("ui.taskStatus.rollOutCloudKit");
    },
    get description() {
      return tr("ui.taskStatus.preparingToolsApplyingTheStackKit");
    },
    taskIds: [
      "simulate_update",
      "stackkit_prepare",
      "docker_ready",
      "opentofu_ready",
      "terramate_ready",
      "telemetry_ready",
      "stackkit_rollout",
      "service_inventory",
    ],
  },
  {
    id: "verify",
    get label() {
      return tr("ui.taskStatus.verifyRollout");
    },
    get description() {
      return tr("ui.taskStatus.confirmingServicesAndValidatingThe");
    },
    taskIds: ["verify_rollout", "restore_drill"],
  },
];

export const POST_LEASE_RUNTIME_TASK_IDS = [
  "prepare_rollout",
  "runtime_connected",
  "telemetry_handshake",
  "validate_workers",
  "generate_unified",
  "persist_unified",
  "generate_iac",
  "simulate_update",
  "stackkit_prepare",
  "docker_ready",
  "opentofu_ready",
  "terramate_ready",
  "telemetry_ready",
  "stackkit_rollout",
  "service_inventory",
  "verify_rollout",
  "restore_drill",
] as const;

export const STACKKIT_ARTIFACT_OR_ROUTING_TASK_IDS = [
  "generate_unified",
  "persist_unified",
  "generate_iac",
  "simulate_update",
  "stackkit_prepare",
  "docker_ready",
  "opentofu_ready",
  "terramate_ready",
  "telemetry_ready",
  "stackkit_rollout",
  "service_inventory",
  "verify_rollout",
  "restore_drill",
] as const;

const postLeaseRuntimeTaskIds = new Set<string>(POST_LEASE_RUNTIME_TASK_IDS);
const stackKitArtifactOrRoutingTaskIds = new Set<string>(
  STACKKIT_ARTIFACT_OR_ROUTING_TASK_IDS,
);

export function isPostLeaseRuntimeTask(taskId: string | undefined): boolean {
  return Boolean(taskId && postLeaseRuntimeTaskIds.has(taskId));
}

export function isStackKitArtifactOrRoutingTask(
  taskId: string | undefined,
): boolean {
  return Boolean(taskId && stackKitArtifactOrRoutingTaskIds.has(taskId));
}

export interface GroupStatus {
  group: TaskGroup;
  tasks: Task[];
  status: Task["status"];
  completed: number;
  total: number;
  runningTask?: Task;
  failedTask?: Task;
}

/**
 * Compute the aggregated status for a single group from the current task list.
 * A group is failed if any sub-task failed; running if any sub-task is running;
 * completed if every sub-task is completed; pending otherwise. Sub-tasks that
 * are not present in the current task list (e.g. RUNTIME_TASKS on a non-cloud
 * flow) are dropped from `tasks` and `total`.
 */
export function computeGroupStatus(
  tasks: Task[],
  group: TaskGroup,
): GroupStatus {
  const taskById = new Map(tasks.map((t) => [t.id, t]));
  const groupTasks = group.taskIds
    .map((id) => taskById.get(id))
    .filter((t): t is Task => Boolean(t));
  const visibleIds = group.visibleTaskIds ?? group.taskIds;
  const visibleTasks = visibleIds
    .map((id) => taskById.get(id))
    .filter((t): t is Task => Boolean(t));

  const total = groupTasks.length;
  const completed = groupTasks.filter((t) => t.status === "completed").length;
  const failedTask = groupTasks.find((t) => t.status === "failed");
  const runningTask = groupTasks.find((t) => t.status === "running");

  let status: Task["status"] = "pending";
  if (total > 0) {
    if (failedTask) status = "failed";
    else if (runningTask) status = "running";
    else if (completed === total) status = "completed";
  }

  return {
    group,
    tasks: visibleTasks,
    status,
    completed,
    total,
    runningTask,
    failedTask,
  };
}

/**
 * Compute per-phase status for the full list of groups. Groups that have no
 * matching tasks in the current list are skipped so the UI does not render
 * empty phase cards on the non-cloud (Unifier-only) flow.
 */
export function computeGroupStatuses(tasks: Task[]): GroupStatus[] {
  return TASK_GROUPS.map((group) => computeGroupStatus(tasks, group)).filter(
    (gs) => gs.total > 0,
  );
}

/**
 * Create initial task list with pending status
 */
export function createTaskList(): Task[] {
  return DEFAULT_TASKS.map((t) => ({
    ...t,
    status: "pending" as const,
  }));
}

export function createRuntimeTaskList(): Task[] {
  return [...DEFAULT_TASKS, ...RUNTIME_TASKS].map((t) => ({
    ...t,
    status: "pending" as const,
  }));
}

export function createAddServerTaskList(
  serverProvisioningMode: string | undefined,
): Task[] {
  const definitions =
    serverProvisioningMode === "kombify-cloud"
      ? ADD_SERVER_MANAGED_RUNTIME_TASKS
      : ADD_SERVER_REGISTRATION_TASKS;
  return definitions.map((t) => ({
    ...t,
    status: "pending" as const,
  }));
}

/**
 * Map backend job status to task status
 */
export function mapJobStatusToTaskStatus(jobStatus: string): Task["status"] {
  switch (jobStatus) {
    case "pending":
    case "queued":
      return "pending";
    case "running":
    case "in_progress":
    case "waiting":
      return "running";
    case "completed":
    case "success":
      return "completed";
    case "failed":
    case "error":
      return "failed";
    default:
      return "pending";
  }
}
