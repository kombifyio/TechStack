<script lang="ts">
  import { page } from '$app/state';
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import {
    getWalletItemsByKitDeployment,
    createWalletItem,
    updateWalletItem,
    deleteWalletItem,
  } from "#lib/api/wallet.js";
  import type { WalletItem } from "#lib/wallet/types.js";
  import { parseApiError } from "#lib/api/errors.js";
  import { buildWalletEntryPayload } from "#lib/wallet/payload.js";
  import CredentialForm from "#lib/components/CredentialForm.svelte";
  import Modal from "#lib/components/Modal.svelte";
  import { discoverServiceCredentials, type DiscoveredCredential } from "#lib/wallet/integration.js";

  import {
    decommissionMonthlyRuntime,
    disableMonthlyRuntimeSSH,
    enableMonthlyRuntimeSSH,
    startMonthlyRuntime,
    stopMonthlyRuntime,
    getStackOperations,
    getMonthlyRuntimeOfferings,
    getMonthlyRuntimeOperations,
    getMonthlyRuntimeSSHInfo,
    getMonthlyRuntimeStatus,
    type KitDeployment,
    type StackOperationService,
    type StackOperationsPayload,
    type MonthlyRuntimeOffering,
    type MonthlyRuntimeOperation,
    type MonthlyRuntimeStatus,
  } from "#lib/api/stacks.js";
  import { listServiceRegistry, type RegistryService } from "#lib/api/registry.js";
  import {
    openServiceUrl,
    serviceCardName,
    serviceCardPlacement,
    serviceCardStatus,
    serviceCardStatusMessage,
    serviceTargetLabel,
    type TechStackServiceCardSource,
  } from "#lib/service-card-adapter.js";
  import { ServiceCard } from "@kombiverselabs/ui/service";
  import { confirmInApp } from "#lib/dialogs/in-app-dialog.js";
  import ManagedRuntimeRecreatePanel from "#lib/components/managed-runtime/ManagedRuntimeRecreatePanel.svelte";

  // Stack data
  let stack = $state<KitDeployment | null>(null);
  let stackOperations = $state<StackOperationsPayload | null>(null);
  type DeploymentService = TechStackServiceCardSource & {
    id: string;
    name: string;
    type: string;
    status: string;
  };

  let services = $state<DeploymentService[]>([]);
  let credentials = $state<WalletItem[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let runtimeStatus = $state<MonthlyRuntimeStatus | null>(null);
  let runtimeOfferings = $state<MonthlyRuntimeOffering[]>([]);
  let runtimeOperations = $state<MonthlyRuntimeOperation[]>([]);
  let runtimeLoading = $state(false);
  let runtimeError = $state<string | null>(null);
  let runtimeBusyAction = $state<string | null>(null);
  let runtimeDecommissionRequested = $state(false);

  // Modal states
  let showCredentialForm = $state(false);
  let editingCredential = $state<WalletItem | null>(null);
  let saving = $state(false);

  // Auto-discovered credentials that can be added
  let discoveredCredentials = $state<DiscoveredCredential[]>([]);

  const stackId = $derived(page.params.id);
  const isMonthlyRuntimeStack = $derived(
    Boolean(
      stack?.lease_id ||
      stack?.runtime_lane === "monthly" ||
      stack?.runtime_lane === "monthly-runtime" ||
      stack?.server_mode === "monthly" ||
      stack?.server_mode === "monthly-runtime",
    ),
  );
  const selectedRuntimeOffering = $derived(
    runtimeOfferings.find(
      (offering) =>
        offering.id ===
        (stack?.runtime_offering_id || runtimeStatus?.runtime_offering_id),
    ) || null,
  );
  const primaryRuntimeCleanupStarted = $derived(
    Boolean(
      stack?.lease_id &&
      (runtimeDecommissionRequested ||
        stackOperations?.servers?.some(
          (server) =>
            server.lease_id === stack?.lease_id &&
            ["decommissioning", "decommissioned"].includes(
              server.capabilities?.lifecycle_state || "",
            ),
        ) ||
        stackOperations?.retiredServers?.some(
          (server) => server.lease_id === stack?.lease_id,
        )),
    ),
  );
  const additionalRetiredRuntimes = $derived(
    (stackOperations?.retiredServers || []).filter(
      (server) => server.lease_id && server.lease_id !== stack?.lease_id,
    ),
  );

  onMount(() => {
    loadData();
  });

  // Reload when stack ID changes
  $effect(() => {
    if (stackId) {
      loadData();
    }
  });

  async function loadData() {
    if (!stackId) {
      error = "No StackKit deployment ID provided";
      loading = false;
      return;
    }

    loading = true;
    error = null;

    try {
      const loaded = await loadStackRecord(stackId);
      stack = loaded.stack;
      stackOperations = loaded.operations;
      await loadMonthlyRuntimePanel(stack);

      services = servicesFromOperations(loaded.operations);
      if (services.length === 0) {
        try {
          services = (await listServiceRegistry()).services
            .filter((service) => service.kit_deployment_id === stackId)
            .map(registryServiceToDeploymentService);
        } catch (serviceErr) {
          if (!loaded.operations) throw serviceErr;
          services = [];
        }
      }

      try {
        credentials = await getWalletItemsByKitDeployment(stackId);
      } catch (walletErr) {
        if (!loaded.operations) throw walletErr;
        credentials = [];
      }

      discoverMissingCredentials();
    } catch (err) {
      stackOperations = null;
      const parsed = parseApiError(err);
      error = parsed.message || "Failed to load StackKit deployment";
      if (parsed.isNotFound) {
        goto("/dashboard");
      }
    } finally {
      loading = false;
    }
  }

  async function loadStackRecord(
    id: string,
  ): Promise<{
    stack: KitDeployment;
    operations: StackOperationsPayload | null;
  }> {
    const operations = await getStackOperations(id);
    return {
      stack: operations.stack,
      operations,
    };
  }

  function servicesFromOperations(
    operations: StackOperationsPayload | null,
  ): DeploymentService[] {
    return (operations?.services || []).map(operationServiceToDeploymentService);
  }

  function operationServiceToDeploymentService(
    service: StackOperationService,
  ): DeploymentService {
    return {
      id:
        service.id ||
        `${stackId}-${service.target_server_id || service.target_server || "server"}-${service.name}`,
      name: service.name,
      display_name: service.display_name,
      type: service.type,
      url: service.url,
      port: service.port,
      status: service.status,
      node_id: service.target_server_id || service.target_server || "",
    };
  }

  function registryServiceToDeploymentService(
    service: RegistryService,
  ): DeploymentService {
    return {
      id:
        service.id ||
        `${service.kit_deployment_id}-${service.server_id}-${service.name}`,
      name: service.name,
      display_name: service.display_name,
      type: service.type,
      port: service.port,
      url: service.url,
      status: service.status,
      node_id: service.server_id,
    };
  }

  async function loadMonthlyRuntimePanel(
    currentStack: KitDeployment | null = stack,
  ) {
    runtimeStatus = null;
    runtimeOperations = [];
    runtimeError = null;
    if (!currentStack?.lease_id) {
      return;
    }

    runtimeLoading = true;
    try {
      const [offerings, status, operations] = await Promise.all([
        getMonthlyRuntimeOfferings(),
        getMonthlyRuntimeStatus(currentStack.lease_id),
        getMonthlyRuntimeOperations(currentStack.lease_id),
      ]);
      runtimeOfferings = offerings;
      runtimeStatus = status;
      runtimeOperations = operations;
    } catch (err) {
      runtimeError =
        err instanceof Error ? err.message : "Failed to load runtime status";
    } finally {
      runtimeLoading = false;
    }
  }

  async function runRuntimeAction(
    action:
      "start" |
      "stop" |
      "enable-ssh" |
      "disable-ssh" |
      "ssh" |
      "decommission"
  ) {
    if (!stack?.lease_id) return;
    const leaseID = stack.lease_id;

    if (action === "stop" && !await confirmInApp({
      title: "Stop managed server?",
        message:
          "The server shuts down and keeps its disk, address and monthly plan. Start it again at any time.",
      confirmText: "Stop",
      tone: "danger"
    })) {
      return;
    }
    if (action === "decommission" && !await confirmInApp({
      title: "Decommission managed runtime?",
        message:
          "Techstack will begin provider cleanup for this managed runtime.",
      confirmText: "Decommission",
      tone: "danger"
    })) {
      return;
    }
    runtimeBusyAction = action;
    runtimeError = null;
    try {
      switch (action) {
        case "start":
          runtimeStatus = await startMonthlyRuntime(leaseID);
          break;
        case "stop":
          runtimeStatus = await stopMonthlyRuntime(leaseID);
          break;
        case "enable-ssh":
          runtimeStatus = await enableMonthlyRuntimeSSH(leaseID);
          break;
        case "disable-ssh":
          runtimeStatus = await disableMonthlyRuntimeSSH(leaseID);
          break;
        case "ssh":
          runtimeStatus = await getMonthlyRuntimeSSHInfo(leaseID);
          break;
        case "decommission":
          runtimeStatus = await decommissionMonthlyRuntime(leaseID);
          runtimeDecommissionRequested = true;
          stack = { ...stack, desired_state: "stopped" };
          stackOperations = await getStackOperations(stackId || stack.id);
          break;
      }
      runtimeOperations = await getMonthlyRuntimeOperations(leaseID);
    } catch (err) {
      runtimeError =
        err instanceof Error ? err.message : "Monthly Runtime action failed";
    } finally {
      runtimeBusyAction = null;
    }
  }

  function discoverMissingCredentials() {
    const existingServiceIds = new Set(
      credentials.filter((c) => c.service_id).map((c) => c.service_id),
    );

    const discovered: DiscoveredCredential[] = [];
    for (const service of services) {
      if (!existingServiceIds.has(service.id)) {
        const serviceCredentials = discoverServiceCredentials(service, stack!);
        discovered.push(...serviceCredentials);
      }
    }
    discoveredCredentials = discovered;
  }

  function openAddCredential() {
    editingCredential = null;
    showCredentialForm = true;
  }

  function openEditCredential(cred: WalletItem) {
    editingCredential = cred;
    showCredentialForm = true;
  }

  async function handleSaveCredential(data: Partial<WalletItem>) {
    saving = true;
    try {
      if (editingCredential) {
        await updateWalletItem(editingCredential.id, data);
      } else {
        await createWalletItem(
          buildWalletEntryPayload("recovery", {
            ...data,
            kit_deployment_id: stackId,
          }),
        );
      }
      showCredentialForm = false;
      editingCredential = null;
      await loadData();
    } finally {
      saving = false;
    }
  }

  async function handleDeleteCredential(id: string) {
    if (!await confirmInApp({
      title: "Delete credential?",
      message: "This permanently removes the selected credential.",
      confirmText: "Delete",
      tone: "danger"
    })) return;

    try {
      await deleteWalletItem(id);
      await loadData();
    } catch (err) {
      const parsed = parseApiError(err);
      error = parsed.message || "Failed to delete credential";
    }
  }

  async function addDiscoveredCredential(discovered: DiscoveredCredential) {
    saving = true;
    try {
      await createWalletItem(
        buildWalletEntryPayload("recovery", {
        name: discovered.name,
        kind: discovered.kind,
        username: discovered.username,
        url: discovered.url,
        notes: discovered.notes,
        service_id: discovered.service_id,
        kit_deployment_id: stackId,
        secret: "" // User must fill this in
      }));

      await loadData();
    } catch (err) {
      const parsed = parseApiError(err);
      error = parsed.message || "Failed to add credential";
    } finally {
      saving = false;
    }
  }

  function getKindBadgeClass(kind: string): string {
    switch (kind) {
      case "password":
        return "bg-primary/10 text-primary";
      case "api_key":
        return "bg-info/10 text-info";
      case "ssh_key":
        return "bg-success/10 text-success";
      case "oauth_token":
        return "bg-warning/10 text-warning";
      case "certificate":
        return "bg-destructive/10 text-destructive";
      default:
        return "bg-muted text-muted-foreground";
    }
  }

  function formatKind(kind: string): string {
    switch (kind) {
      case "password":
        return "Password";
      case "api_key":
        return "API Key";
      case "ssh_key":
        return "SSH Key";
      case "oauth_token":
        return "OAuth";
      case "certificate":
        return "Certificate";
      default:
        return kind;
    }
  }

  function hasSecret(cred: WalletItem): boolean {
    return Boolean(cred.has_secret || cred.has_totp || cred.secret && cred.secret.trim().length > 0);
  }

  function runtimeLabel(value?: string | null): string {
    return value ? value.replace(/[-_]/g, " ") : "unknown";
  }

  function runtimeStateLabel(): string {
    return runtimeLabel(
      runtimeStatus?.status?.state ||
        runtimeStatus?.desired_state ||
        stack?.desired_state ||
        stack?.runtime_phase,
    );
  }

  function sshStateLabel(): string {
    const enabled =
      runtimeStatus?.ssh_access?.enabled ??
      runtimeStatus?.ssh_enabled ??
      runtimeStatus?.ssh?.enabled ??
      runtimeStatus?.status?.ssh_enabled;
    if (enabled === true) return "enabled";
    if (enabled === false) return "disabled";
    return "unknown";
  }

  function formatRuntimeTimestamp(value?: string): string {
    if (!value) return "";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;
    return date.toLocaleString();
  }
</script>

<svelte:head>
  <title
    >{stack?.name || "StackKit Deployment"} - Credentials | kombify-Techstack</title
  >
</svelte:head>

<div class="bg-background min-h-full">
  <div class="container mx-auto px-4 py-8 max-w-6xl">
    <!-- Header -->
    <div class="flex items-center justify-between mb-8">
      <div>
        <a
          href="/dashboard"
          class="text-primary hover:text-primary/80 text-sm mb-2 inline-block"
        >
          ← Back to Homelab
        </a>
        <h1 class="text-3xl font-bold text-foreground">
          {stack?.name || "Loading..."}
        </h1>
        <p class="text-muted-foreground mt-1">
          Manage credentials for this StackKit deployment
        </p>
      </div>
      <button
        onclick={openAddCredential}
        class="bg-primary hover:bg-primary/80 text-primary-foreground px-4 py-2 rounded-lg flex items-center gap-2"
      >
        <svg
          class="w-5 h-5"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M12 4v16m8-8H4"
          ></path>
        </svg>
        Add Credential
      </button>
    </div>

    {#if loading}
      <div class="flex justify-center py-12">
        <div
          class="animate-spin rounded-full h-12 w-12 border-b-2 border-primary"
        ></div>
      </div>
    {:else if error}
      <div
        class="bg-destructive/10 border border-destructive/30 rounded-xl p-4 text-destructive"
      >
        {error}
      </div>
    {:else}
      {#if isMonthlyRuntimeStack}
        <div
          data-kx="plate"
          class="overflow-hidden mb-6"
          data-testid="monthly-runtime-card"
        >
          <div
            class="px-6 py-4 border-b border-border flex flex-col gap-3 md:flex-row md:items-center md:justify-between"
          >
            <div>
              <h2 class="text-lg font-semibold text-foreground">
                Monthly Runtime
              </h2>
              <p class="text-sm text-muted-foreground">
                {stack?.lease_id || "No lease attached"}
              </p>
            </div>
            <div class="flex flex-wrap gap-2">
              <button
                data-testid="monthly-runtime-action-start"
                onclick={() => runRuntimeAction("start")}
                disabled={!stack?.lease_id ||
                  runtimeLoading ||
                  Boolean(runtimeBusyAction)}
                class="px-3 py-2 text-sm rounded-lg bg-muted text-foreground hover:bg-muted/80 disabled:opacity-50"
              >
                Start
              </button>
              <button
                data-testid="monthly-runtime-action-stop"
                onclick={() => runRuntimeAction("stop")}
                disabled={!stack?.lease_id ||
                  runtimeLoading ||
                  Boolean(runtimeBusyAction)}
                class="px-3 py-2 text-sm rounded-lg bg-muted text-foreground hover:bg-muted/80 disabled:opacity-50"
              >
                Stop
              </button>
              <button
                data-testid="monthly-runtime-action-enable-ssh"
                onclick={() => runRuntimeAction("enable-ssh")}
                disabled={!stack?.lease_id ||
                  runtimeLoading ||
                  Boolean(runtimeBusyAction)}
                class="px-3 py-2 text-sm rounded-lg bg-muted text-foreground hover:bg-muted/80 disabled:opacity-50"
              >
                SSH On
              </button>
              <button
                data-testid="monthly-runtime-action-disable-ssh"
                onclick={() => runRuntimeAction("disable-ssh")}
                disabled={!stack?.lease_id ||
                  runtimeLoading ||
                  Boolean(runtimeBusyAction)}
                class="px-3 py-2 text-sm rounded-lg bg-muted text-foreground hover:bg-muted/80 disabled:opacity-50"
              >
                SSH Off
              </button>
              <button
                data-testid="monthly-runtime-action-ssh"
                onclick={() => runRuntimeAction("ssh")}
                disabled={!stack?.lease_id ||
                  runtimeLoading ||
                  Boolean(runtimeBusyAction)}
                class="px-3 py-2 text-sm rounded-lg bg-muted text-foreground hover:bg-muted/80 disabled:opacity-50"
              >
                SSH Info
              </button>
              <button
                data-testid="monthly-runtime-action-decommission"
                onclick={() => runRuntimeAction("decommission")}
                disabled={!stack?.lease_id ||
                  runtimeLoading ||
                  Boolean(runtimeBusyAction)}
                class="px-3 py-2 text-sm rounded-lg bg-destructive/10 text-destructive hover:bg-destructive/20 disabled:opacity-50"
              >
                Decommission
              </button>
            </div>
          </div>

          <div class="grid gap-4 p-6 md:grid-cols-4">
            <div data-testid="monthly-runtime-offering">
              <p class="text-xs uppercase tracking-wider text-muted-foreground">
                Offering
              </p>
              <p class="mt-1 font-medium text-foreground">
                {selectedRuntimeOffering?.name ||
                  stack?.runtime_offering_id ||
                  runtimeStatus?.runtime_offering_id ||
                  "unknown"}
              </p>
              {#if selectedRuntimeOffering}
                <p class="text-sm text-muted-foreground">
                  {selectedRuntimeOffering.vcpus || 0} vCPU · {Math.round(
                    (selectedRuntimeOffering.memory_mb || 0) / 1024,
                  )} GB RAM
                </p>
              {/if}
            </div>
            <div data-testid="monthly-runtime-enrollment">
              <p class="text-xs uppercase tracking-wider text-muted-foreground">
                Enrollment
              </p>
              <p class="mt-1 font-medium text-foreground">
                {runtimeLabel(stack?.verification_status)}
                {#if runtimeStatus?.enrollment_status}
                  <span class="block text-sm text-muted-foreground">
                    {runtimeLabel(runtimeStatus.enrollment_status)}
                  </span>
                {/if}
              </p>
            </div>
            <div data-testid="monthly-runtime-state">
              <p class="text-xs uppercase tracking-wider text-muted-foreground">
                Runtime
              </p>
              <p class="mt-1 font-medium text-foreground">
                {runtimeLoading ? "loading" : runtimeStateLabel()}
              </p>
            </div>
            <div data-testid="monthly-runtime-ssh">
              <p class="text-xs uppercase tracking-wider text-muted-foreground">
                SSH
              </p>
              <p class="mt-1 font-medium text-foreground">{sshStateLabel()}</p>
            </div>
          </div>

          {#if runtimeBusyAction}
            <div class="px-6 pb-4 text-sm text-muted-foreground">
              {runtimeLabel(runtimeBusyAction)} pending
            </div>
          {/if}
          {#if runtimeError}
            <div class="px-6 pb-4 text-sm text-destructive">
              {runtimeError}
            </div>
          {/if}

          {#if runtimeOperations.length > 0}
            <div
              class="border-t border-border px-6 py-4"
              data-testid="monthly-runtime-operations"
            >
              <h3 class="text-sm font-semibold text-foreground mb-3">
                Operations
              </h3>
              <div class="space-y-2">
                {#each runtimeOperations as operation}
                  <div
                    class="flex flex-col gap-1 rounded-lg border border-border bg-background/50 px-3 py-2 md:flex-row md:items-center md:justify-between"
                  >
                    <div>
                      <span class="text-sm font-medium text-foreground">
                        {runtimeLabel(operation.status)}
                      </span>
                      <span class="ml-2 text-xs text-muted-foreground">
                        {runtimeLabel(operation.event_type)}
                      </span>
                      {#if operation.error}
                        <p class="text-xs text-destructive mt-1">
                          {operation.error}
                        </p>
                      {/if}
                    </div>
                    <div class="text-xs text-muted-foreground md:text-right">
                      {#if operation.actor}
                        <span>{operation.actor}</span>
                      {/if}
                      <span class="block">
                        {formatRuntimeTimestamp(operation.created_at)}
                      </span>
                    </div>
                  </div>
                {/each}
              </div>
            </div>
          {/if}
        </div>
      {/if}

      {#if primaryRuntimeCleanupStarted && stack?.lease_id}
        <div class="mb-6">
          <ManagedRuntimeRecreatePanel
            stackId={stackId || ""}
            leaseId={stack.lease_id}
            serverName={stack.name || "managed server"}
          />
        </div>
      {/if}

      {#each additionalRetiredRuntimes as retired (retired.id)}
        <div class="mb-6">
          <ManagedRuntimeRecreatePanel
            stackId={stackId || ""}
            leaseId={retired.lease_id || ""}
            serverName={retired.hostname || "managed server"}
          />
        </div>
      {/each}

      {#if stackOperations?.monitoring}
        <div
          data-kx="plate"
          class="overflow-hidden mb-6"
          data-testid="stack-detail-monitoring-evidence"
        >
          <div class="px-6 py-4 border-b border-border">
            <h2 class="text-lg font-semibold text-foreground">
              Monitoring Evidence
            </h2>
            <p class="text-sm text-muted-foreground">
              {stackOperations.monitoring.message ||
                "Latest operations snapshot"}
            </p>
          </div>
          <div class="grid gap-4 p-6 md:grid-cols-4">
            <div>
              <p class="text-xs uppercase tracking-wider text-muted-foreground">
                Status
              </p>
              <p class="mt-1 font-medium text-foreground">
                {runtimeLabel(stackOperations.monitoring.status)}
              </p>
            </div>
            <div>
              <p class="text-xs uppercase tracking-wider text-muted-foreground">
                Query
              </p>
              <p class="mt-1 font-medium text-foreground">
                {stackOperations.monitoring.queryBackend}
              </p>
            </div>
            <div>
              <p class="text-xs uppercase tracking-wider text-muted-foreground">
                Ingest
              </p>
              <p class="mt-1 font-medium text-foreground">
                {stackOperations.monitoring.ingestBackend}
              </p>
            </div>
            <div>
              <p class="text-xs uppercase tracking-wider text-muted-foreground">
                Series
              </p>
              <p class="mt-1 font-medium text-foreground">
                {stackOperations.monitoring.seriesCount ?? 0}
              </p>
            </div>
          </div>
        </div>
      {/if}

      <!-- Discovered Credentials Section -->
      {#if discoveredCredentials.length > 0}
        <div
          class="bg-warning/10 border border-warning/30 rounded-xl p-4 mb-6"
        >
          <h3 class="font-semibold text-warning mb-2 flex items-center gap-2">
            <svg
              class="w-5 h-5"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
              ></path>
            </svg>
            Missing Credentials Detected
          </h3>
          <p class="text-warning/80 text-sm mb-3">
            The following services need credentials to be configured:
          </p>
          <div class="space-y-2">
            {#each discoveredCredentials as discovered}
              <div
                class="flex items-center justify-between bg-card rounded-lg p-3 border border-warning/30"
              >
                <div>
                  <span class="font-medium text-foreground"
                    >{discovered.name}</span
                  >
                  <span
                    class="ml-2 text-xs px-2 py-1 rounded-full {getKindBadgeClass(
                      discovered.kind,
                    )}"
                  >
                    {formatKind(discovered.kind)}
                  </span>
                </div>
                <button
                  onclick={() => addDiscoveredCredential(discovered)}
                  disabled={saving}
                  class="text-primary hover:text-primary/80 text-sm font-medium disabled:opacity-50"
                >
                  + Add
                </button>
              </div>
            {/each}
          </div>
        </div>
      {/if}

      <!-- Credentials Table -->
      <div data-kx="plate" class="overflow-hidden">
        <div class="px-6 py-4 border-b border-border">
          <h2 class="text-lg font-semibold text-foreground">
            Stored Credentials
          </h2>
          <p class="text-sm text-muted-foreground">
            {credentials.length} credential{credentials.length !== 1 ? "s" : ""} configured
          </p>
        </div>

        {#if credentials.length === 0}
          <div class="p-8 text-center text-muted-foreground">
            <svg
              class="w-12 h-12 mx-auto mb-4 text-muted-foreground/50"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M15 7a2 2 0 012 2m4 0a6 6 0 01-7.743 5.743L11 17H9v2H7v2H4a1 1 0 01-1-1v-2.586a1 1 0 01.293-.707l5.964-5.964A6 6 0 1121 9z"
              ></path>
            </svg>
            <p>No credentials configured for this StackKit deployment.</p>
            <p class="text-sm mt-1">
              Add credentials to enable auto-login and secure storage.
            </p>
          </div>
        {:else}
          <table class="min-w-full divide-y divide-border">
            <thead class="bg-muted/50">
              <tr>
                <th
                  class="px-6 py-3 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                  >Name</th
                >
                <th
                  class="px-6 py-3 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                  >Type</th
                >
                <th
                  class="px-6 py-3 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                  >Username</th
                >
                <th
                  class="px-6 py-3 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                  >Status</th
                >
                <th
                  class="px-6 py-3 text-right text-xs font-medium text-muted-foreground uppercase tracking-wider"
                  >Actions</th
                >
              </tr>
            </thead>
            <tbody class="divide-y divide-border">
              {#each credentials as cred}
                <tr class="hover:bg-muted/50">
                  <td class="px-6 py-4 whitespace-nowrap">
                    <div class="font-medium text-foreground">{cred.name}</div>
                    {#if cred.url}
                      <div
                        class="text-sm text-muted-foreground truncate max-w-xs"
                      >
                        {cred.url}
                      </div>
                    {/if}
                  </td>
                  <td class="px-6 py-4 whitespace-nowrap">
                    <span
                      class="text-xs px-2 py-1 rounded-full {getKindBadgeClass(
                        cred.kind,
                      )}"
                    >
                      {formatKind(cred.kind)}
                    </span>
                  </td>
                  <td
                    class="px-6 py-4 whitespace-nowrap text-sm text-muted-foreground"
                  >
                    {cred.username || "-"}
                  </td>
                  <td class="px-6 py-4 whitespace-nowrap">
                    {#if hasSecret(cred)}
                      <span
                        class="inline-flex items-center text-success text-sm"
                      >
                        <svg
                          class="w-4 h-4 mr-1"
                          fill="currentColor"
                          viewBox="0 0 20 20"
                        >
                          <path
                            fill-rule="evenodd"
                            d="M10 18a8 8 0 100-16 8 8 0 000 16zm3.707-9.293a1 1 0 00-1.414-1.414L9 10.586 7.707 9.293a1 1 0 00-1.414 1.414l2 2a1 1 0 001.414 0l4-4z"
                            clip-rule="evenodd"
                          ></path>
                        </svg>
                        Configured
                      </span>
                    {:else}
                      <span
                        class="inline-flex items-center text-warning text-sm"
                      >
                        <svg
                          class="w-4 h-4 mr-1"
                          fill="currentColor"
                          viewBox="0 0 20 20"
                        >
                          <path
                            fill-rule="evenodd"
                            d="M8.257 3.099c.765-1.36 2.722-1.36 3.486 0l5.58 9.92c.75 1.334-.213 2.98-1.742 2.98H4.42c-1.53 0-2.493-1.646-1.743-2.98l5.58-9.92zM11 13a1 1 0 11-2 0 1 1 0 012 0zm-1-8a1 1 0 00-1 1v3a1 1 0 002 0V6a1 1 0 00-1-1z"
                            clip-rule="evenodd"
                          ></path>
                        </svg>
                        Missing Secret
                      </span>
                    {/if}
                  </td>
                  <td class="px-6 py-4 whitespace-nowrap text-right text-sm">
                    <button
                      onclick={() => openEditCredential(cred)}
                      class="text-primary hover:text-primary/80 mr-3"
                    >
                      Edit
                    </button>
                    <button
                      onclick={() => handleDeleteCredential(cred.id)}
                      class="text-destructive hover:text-destructive/80"
                    >
                      Delete
                    </button>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </div>

      <!-- Services Section -->
      {#if services.length > 0}
        <div
          data-kx="plate"
          class="mt-8 overflow-hidden"
          data-testid="stack-detail-service-registry"
        >
          <div class="px-6 py-4 border-b border-border">
            <h2 class="text-lg font-semibold text-foreground">
              StackKit Deployment Services
            </h2>
            <p class="text-sm text-muted-foreground">
              {services.length} service{services.length !== 1 ? "s" : ""} deployed
            </p>
          </div>
          <div class="grid gap-3 p-6 md:grid-cols-2">
            {#each services as service}
              <ServiceCard
                name={serviceCardName(service)}
                description={service.url ||
                  serviceTargetLabel(service) ||
                  "StackKit service"}
                placement={serviceCardPlacement(service)}
                status={serviceCardStatus(service)}
                statusMessage={serviceCardStatusMessage(service)}
                onOpen={service.url ? () => openServiceUrl(service) : undefined}
              />
            {/each}
          </div>
        </div>
      {/if}
    {/if}
  </div>
</div>

<!-- Credential Form Modal -->
{#if showCredentialForm}
  <Modal
    title={editingCredential ? "Edit Credential" : "Add Credential"}
    onClose={() => {
      showCredentialForm = false;
      editingCredential = null;
    }}
  >
    <CredentialForm
      credential={editingCredential || undefined}
      onSave={handleSaveCredential}
      onCancel={() => {
        showCredentialForm = false;
        editingCredential = null;
      }}
      saving={saving}
    />
  </Modal>
{/if}
