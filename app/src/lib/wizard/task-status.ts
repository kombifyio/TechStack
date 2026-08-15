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
    label: "Validating configuration",
    labelKey: "tasks.validate",
  },
  {
    id: "save_config",
    label: "Saving your configuration",
    labelKey: "tasks.save_config",
  },
  {
    id: "find_stackkit",
    label: "Finding the best StackKit for you",
    labelKey: "tasks.find_stackkit",
  },
  {
    id: "unify_services",
    label: "Identifying services & best practices",
    labelKey: "tasks.unify_services",
  },
  {
    id: "unify_network",
    label: "Configuring network settings",
    labelKey: "tasks.unify_network",
  },
  {
    id: "unify_security",
    label: "Setting up security configuration",
    labelKey: "tasks.unify_security",
  },
  {
    id: "unify_auth",
    label: "Configuring authentication",
    labelKey: "tasks.unify_auth",
  },
  {
    id: "create_spec",
    label: "Creating your deployment spec",
    labelKey: "tasks.create_spec",
  },
];

export const RUNTIME_TASKS: Omit<Task, "status">[] = [
  {
    id: "create_lease",
    label: "Requesting managed cloud server",
    labelKey: "tasks.runtime.create_lease",
  },
  {
    id: "prepare_rollout",
    label: "Confirming VPS target",
    labelKey: "tasks.runtime.prepare_rollout",
  },
  {
    id: "runtime_connected",
    label: "Connecting to runtime",
    labelKey: "tasks.runtime.runtime_connected",
  },
  {
    id: "telemetry_handshake",
    label: "Starting telemetry handoff",
    labelKey: "tasks.runtime.telemetry_handshake",
  },
  {
    id: "validate_workers",
    label: "Checking rollout target",
    labelKey: "tasks.runtime.validate_workers",
  },
  {
    id: "generate_unified",
    label: "Generating unified spec",
    labelKey: "tasks.runtime.generate_unified",
  },
  {
    id: "persist_unified",
    label: "Persisting rollout spec",
    labelKey: "tasks.runtime.persist_unified",
  },
  {
    id: "generate_iac",
    label: "Generating StackKit IaC",
    labelKey: "tasks.runtime.generate_iac",
  },
  {
    id: "simulate_update",
    label: "Running simulated update gate",
    labelKey: "tasks.runtime.simulate_update",
  },
  {
    id: "stackkit_prepare",
    label: "Preparing StackKits runtime",
    labelKey: "tasks.runtime.stackkit_prepare",
  },
  {
    id: "docker_ready",
    label: "Installing and checking Docker",
    labelKey: "tasks.runtime.docker_ready",
  },
  {
    id: "opentofu_ready",
    label: "Checking OpenTofu",
    labelKey: "tasks.runtime.opentofu_ready",
  },
  {
    id: "terramate_ready",
    label: "Checking Terramate",
    labelKey: "tasks.runtime.terramate_ready",
  },
  {
    id: "telemetry_ready",
    label: "Preparing telemetry",
    labelKey: "tasks.runtime.telemetry_ready",
  },
  {
    id: "stackkit_rollout",
    label: "Rolling out Cloud Kit",
    labelKey: "tasks.runtime.stackkit_rollout",
  },
  {
    id: "service_inventory",
    label: "Reading service inventory",
    labelKey: "tasks.runtime.service_inventory",
  },
  {
    id: "verify_rollout",
    label: "Verifying login-protected services",
    labelKey: "tasks.runtime.verify_rollout",
  },
  {
    id: "restore_drill",
    label: "Running restore drill",
    labelKey: "tasks.runtime.restore_drill",
  },
];

export const ADD_SERVER_MANAGED_RUNTIME_TASKS: Omit<Task, "status">[] = [
  {
    id: "create_lease",
    label: "Requesting managed server",
    labelKey: "tasks.runtime.create_lease",
  },
];

export const ADD_SERVER_REGISTRATION_TASKS: Omit<Task, "status">[] = [
  {
    id: "create_spec",
    label: "Preparing server registration",
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
    title: "Checking your choices",
    description:
      "Verifying that all selected options are valid and compatible with each other.",
    detail:
      "This checks feature selections, access modes, user configuration, and authentication settings for consistency.",
  },
  save_config: {
    title: "Persisting configuration",
    description:
      "Saving your choices to the database so they can be referenced during deployment.",
    detail:
      "Your stack configuration is stored securely and can be exported or modified later from the dashboard.",
  },
  find_stackkit: {
    title: "Matching a StackKit",
    description:
      "Analyzing your goals to find the best-fitting StackKit template.",
    detail:
      "StackKits are curated infrastructure templates. The system picks one that covers your selected features with minimal overhead.",
  },
  unify_services: {
    title: "Building service list",
    description:
      "Determining which services are needed and applying best-practice defaults.",
    detail:
      "Based on your goals, the Unifier selects containers, sets resource limits, and resolves dependencies between services.",
  },
  unify_network: {
    title: "Setting up networking",
    description:
      "Configuring access profiles, reverse proxy, DNS, and internal networking.",
    detail:
      "Network settings are derived from your access mode. Local-only uses internal Docker networking; remote access adds the lane-appropriate private mesh or managed edge route.",
  },
  unify_security: {
    title: "Applying security policies",
    description:
      "Configuring firewall rules, TLS certificates, and isolation settings.",
    detail:
      "Each service gets scoped permissions. TLS is enabled automatically where possible, and services are isolated by default.",
  },
  unify_auth: {
    title: "Setting up authentication",
    description:
      "Configuring single sign-on, user accounts, and access control.",
    detail:
      "Your chosen auth method is applied across all services. Multi-user setups get group-based permissions automatically.",
  },
  create_spec: {
    title: "Generating deployment spec",
    description:
      "Compiling everything into a final StackKits deployment specification.",
    detail:
      "The spec contains all configuration needed to deploy your homelab. It can be version-controlled and reproduced on any compatible server.",
  },
  create_lease: {
    title: "Requesting managed server",
    description:
      "Creating or binding the subscription VM lease for this StackKit rollout.",
    detail:
      "The lease captures runtime state, billing cadence, and the managed provider that will host the Cloud Kit.",
  },
  prepare_rollout: {
    title: "Confirming VPS target",
    description:
      "Loading the persisted intent and waiting for the managed VPS target to become reachable.",
    detail:
      "This checks that the persisted stack spec and requirements-spec.yaml still match, then confirms the managed VM lease exposes a runtime SSH host or public IP before StackKits artifact generation starts.",
  },
  runtime_connected: {
    title: "Connecting to runtime",
    description:
      "Confirming that the bound managed VPS can be addressed by the StackKits CLI.",
    detail:
      "Once this succeeds, the server projection is kept visible in TechStack even if preparation or rollout fails later.",
  },
  telemetry_handshake: {
    title: "Starting telemetry handoff",
    description:
      "Preparing the runtime metadata used by monitoring, operations, and the Runtime Intelligence Layer.",
    detail:
      "TechStack records the managed target and prepares the orchestration handoff before StackKits performs the Cloud Kit rollout.",
  },
  validate_workers: {
    title: "Checking rollout target",
    description:
      "Ensuring the deployment target satisfies the StackKit runtime requirements.",
    detail:
      "Managed kombify Cloud rollouts use the VM lease target; user-owned rollout targets require approved workers.",
  },
  generate_unified: {
    title: "Generating unified spec",
    description:
      "Combining user intent, StackKit defaults, and runtime information into the final deployment spec.",
    detail:
      "The unified spec is the canonical input for StackKits and runtime verification.",
  },
  persist_unified: {
    title: "Persisting rollout spec",
    description:
      "Saving unified-spec.yaml so the rollout is reproducible and auditable.",
    detail:
      "The persisted spec links back to the requirements file and the original stack specification.",
  },
  generate_iac: {
    title: "Generating StackKit IaC",
    description:
      "Rendering the StackKit infrastructure files needed by the rollout adapter.",
    detail:
      "TechStack consumes StackKit artifacts here; StackKits remains responsible for applying them.",
  },
  simulate_update: {
    title: "Running simulation gate",
    description:
      "Validating the update path before applying the rollout to the managed runtime.",
    detail:
      "The simulation gate protects the first rollout and later update flows from unsafe changes.",
  },
  stackkit_prepare: {
    title: "Preparing StackKits runtime",
    description:
      "Running the StackKits CLI prepare contract on the managed VPS.",
    detail:
      "This non-interactive prep step installs and checks the tools StackKits needs before applying the Cloud Kit.",
  },
  docker_ready: {
    title: "Preparing Docker",
    description:
      "Installing or validating the Docker runtime used by the selected services.",
    detail:
      "If apt or unattended upgrades block package installation, TechStack keeps the bound VM visible and shows the collected diagnostics.",
  },
  opentofu_ready: {
    title: "Checking OpenTofu",
    description: "Verifying the infrastructure toolchain needed by StackKits.",
    detail:
      "OpenTofu readiness is part of the StackKits CLI prep contract, not a separate TechStack-owned bootstrap path.",
  },
  terramate_ready: {
    title: "Checking Terramate",
    description:
      "Checking the Terramate toolchain when the selected StackKit lifecycle needs it.",
    detail:
      "Terramate readiness belongs to StackKits lifecycle preparation; TechStack does not require it for the initial managed VPS lease.",
  },
  telemetry_ready: {
    title: "Preparing telemetry",
    description:
      "Preparing OpenTelemetry handoff data for monitoring and operations.",
    detail:
      "The first rollout records the runtime context that later service cards, metrics, and RIL workflows consume.",
  },
  stackkit_rollout: {
    title: "Rolling out Cloud Kit",
    description:
      "Calling the StackKits runtime action that applies the generated Cloud Kit specification.",
    detail:
      "This is the point where the selected services are installed and configured on the runtime target.",
  },
  service_inventory: {
    title: "Reading service inventory",
    description:
      "Collecting service metadata exposed by the StackKits rollout.",
    detail:
      "The dashboard can show managed services as soon as StackKits exposes them, while later verification continues.",
  },
  verify_rollout: {
    title: "Verifying services",
    description:
      "Checking that login-protected services and monitoring signals are available after rollout.",
    detail:
      "Verification confirms that the deployed stack is usable, not only that files were generated.",
  },
  restore_drill: {
    title: "Running restore drill",
    description:
      "Validating the backup and restore path before the stack is marked verified.",
    detail:
      "A verified stack must have a tested recovery path for the default services.",
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
    label: "Configure your stack",
    description: "Validating choices and building the deployment spec",
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
    label: "Provision managed runtime",
    description:
      "Reserving the subscription VM, connecting runtime, and preparing telemetry",
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
    label: "Generate deployment artifacts",
    description: "Rendering StackKits artifacts after VPS readiness",
    taskIds: ["generate_unified", "persist_unified", "generate_iac"],
    visibleTaskIds: ["generate_unified", "generate_iac"],
  },
  {
    id: "rollout",
    label: "Roll out Cloud Kit",
    description: "Preparing tools, applying the StackKit, and reading services",
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
    label: "Verify rollout",
    description: "Confirming services and validating the restore drill",
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
