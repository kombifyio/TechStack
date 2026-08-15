<script lang="ts">
  import { page } from "$app/stores";
  import { goto } from "$app/navigation";
  import { onMount } from "svelte";
  import { createPairingToken } from "$lib/api/trust";
  import {
    addManagedRuntimeServer,
    getStack,
    type AddManagedRuntimeServerRequest,
    type Stack as StackRecord,
  } from "$lib/api/stacks";
  import { listCanonicalServers, type RegistryStack } from "$lib/api/registry";
  import { parseApiError } from "$lib/api/errors";
  import { ApiRequestError } from "$lib/api/client";
  import { createWizardRun } from "$lib/api/wizardRuns";
  import { buildJoinRunRequest } from "$lib/wizard/wizardRunRequest";
  import { loadFeatures, isNativeV2WizardEnabled } from "$lib/stores/features";
  import {
    getOrCreateManagedRuntimeIdempotency,
    settleManagedRuntimeIdempotency,
    type ManagedRuntimeIdempotencyAttempt,
  } from "$lib/idempotency/managed-runtime";
  import {
    applyServerProvisioningMode,
    createDefaultConfig,
    normalizeIonosDatacenter,
    type StackConfig,
  } from "$lib/wizard";
  import { EasyWizard } from "$lib/components/wizard";
  import { ArrowLeft, Server } from "@lucide/svelte";

  type PairingHandoff = {
    /**
     * Raw registration token. Empty on the wizard-run lane: the token lives
     * only in the pairing job's result and the creating page picks it up on
     * its first poll.
     */
    token: string;
    expiresAt: string;
    startedAt: string;
    expectedDeviceName: string;
    existingServerIds?: string[];
  };

  const stackId = $derived($page.params.id);

  function createAddServerConfig(): StackConfig {
    const next = createDefaultConfig();
    next.serverProvisioning.nodeRole = "worker";
    next.owner.bootstrapMode = "none";
    next.auth.requirePassword = false;
    next.auth.requireMfa = false;
    next.auth.allowPasswordless = false;
    return next;
  }

  let config = $state<StackConfig>(createAddServerConfig());
  let stack = $state<RegistryStack | null>(null);
  let loading = $state(true);
  let preparing = $state(false);
  let error = $state<string | null>(null);
  let existingStackServerIds = $state<string[]>([]);
  let existingServerBaselineKnown = false;
  let requiresProviderReselection = $state(false);
  let reselectedProviderId = $state<"centron" | "ionos" | null>(null);
  let stackDefaultsApplied = false;

  onMount(() => {
    void load();
    void loadFeatures();
  });

  $effect(() => {
    if (!stack || stackDefaultsApplied) return;
    stackDefaultsApplied = true;
    if (stack.stackkit_foundation) {
      config.serverProvisioning.stackkitFoundation =
        stack.stackkit_foundation as StackConfig["kit"];
      config.kit = stack.stackkit_foundation as StackConfig["kit"];
    }
    if (isManagedRuntimeStack(stack)) {
      applyServerProvisioningMode(config, "kombify-cloud");
      if (stack.provider_id === "centron" || stack.provider_id === "ionos") {
        config.providerId = stack.provider_id;
        reselectedProviderId = null;
        requiresProviderReselection = false;
      } else {
        // Historical provider labels are display-only and must never become a
        // fresh executable selection. The user must choose a canonical ID.
        requiresProviderReselection = true;
      }
      config.ionosDatacenter = normalizeIonosDatacenter(
        stack.ionos_datacenter || stack.provider_region,
      );
      if (
        stack.runtime_offering_id === "monthly-runtime-standard" ||
        stack.runtime_offering_id === "monthly-runtime-premium"
      ) {
        config.runtimeOfferingId = stack.runtime_offering_id;
      }
    }
  });

  async function load() {
    loading = true;
    error = null;
    try {
      // Canonical read model: the server baseline comes from /api/v1/servers,
      // the stack shape from /api/v1/stacks. Neither is the legacy registry BFF.
      const servers = await listCanonicalServers(stackId);
      existingStackServerIds = servers
        .filter((server) => server.techstack_id === stackId)
        .map((server) => server.id);
      existingServerBaselineKnown = true;
      stack = await loadStackFallback();
      if (!stack) {
        error = "Stack not found.";
      }
    } catch (err) {
      stack = await loadStackFallback();
      if (!stack) {
        const parsed = parseApiError(err);
        error = parsed.message || "Failed to load Server Registry.";
      }
    } finally {
      loading = false;
    }
  }

  async function loadStackFallback(): Promise<RegistryStack | null> {
    if (!stackId) return null;
    try {
      return stackRecordToRegistryStack(await getStack(stackId));
    } catch {
      return null;
    }
  }

  function stackRecordToRegistryStack(item: StackRecord): RegistryStack {
    return {
      id: item.id,
      name: item.name,
      status: item.state || item.status || "unknown",
      stackkit_foundation:
        item.stackkit_catalog_ref || item.catalog_ref || "basement-kit",
      server_mode: item.server_mode,
      runtime_lane: item.runtime_lane,
      runtime_offering_id: item.runtime_offering_id,
      provider_id: item.provider_id,
      lease_provider: item.lease_provider,
      ionos_datacenter: item.ionos_datacenter,
      provider_region: item.provider_region,
      server_provisioning_mode: item.server_provisioning_mode,
    };
  }

  function isManagedRuntimeStack(item: RegistryStack): boolean {
    return (
      item.server_provisioning_mode === "kombify-cloud" ||
      item.server_mode === "monthly-runtime" ||
      item.server_mode === "managed-cloud" ||
      item.runtime_lane === "monthly-runtime"
    );
  }

  function suggestedDeviceName(source: StackConfig): string {
    const host = source.serverProvisioning.remote.host.trim();
    if (host) return host;
    return `${stack?.name || "stack"}-${source.serverProvisioning.nodeRole}`;
  }

  function selectedServiceKeys(source: StackConfig): string[] {
    return Object.entries(source.services)
      .filter(([, enabled]) => enabled)
      .map(([name]) => name);
  }

  function retryableErrorDetail(details: unknown): boolean | undefined {
    if (
      typeof details === "object" &&
      details !== null &&
      "retryable" in details &&
      typeof (details as { retryable?: unknown }).retryable === "boolean"
    ) {
      return (details as { retryable: boolean }).retryable;
    }
    return undefined;
  }

  function joinIdempotencyStorageKey(id: string): string {
    return `wizardJoinIdempotencyKey:${id}`;
  }

  function joinIdempotencyKey(id: string): string {
    const storageKey = joinIdempotencyStorageKey(id);
    const existing = sessionStorage.getItem(storageKey);
    if (existing) return existing;
    const generated =
      typeof crypto?.randomUUID === "function"
        ? crypto.randomUUID()
        : `join-${Date.now()}-${Math.random().toString(16).slice(2)}`;
    sessionStorage.setItem(storageKey, generated);
    return generated;
  }

  // The facade 409s deployments it cannot join yet (pre-v2 specs, managed
  // runtimes); those fall back to the legacy pairing lane.
  function isWizardJoinFallback(err: unknown): boolean {
    if (!(err instanceof ApiRequestError) || err.status !== 409) return false;
    const details = err.details as Record<string, unknown> | undefined;
    const nested = details?.details as Record<string, unknown> | undefined;
    const reason = String(details?.reason_code ?? nested?.reason_code ?? "");
    return (
      reason === "wizard_join_requires_native_v2" ||
      reason === "wizard_join_managed_deferred"
    );
  }

  // Native v2 join lane: one wizard-run request appends the node to the
  // deployment's v2 spec (validated by the pinned StackKits CLI) and mints
  // the pairing job. Returns null when the deployment needs the legacy lane.
  async function prepareViaWizardRun(
    wizardConfig: StackConfig,
    currentStackId: string,
  ): Promise<{ jobId: string; pairingHandoff: PairingHandoff } | null> {
    const request = buildJoinRunRequest(
      wizardConfig,
      currentStackId,
      stack?.name || "",
      selectedServiceKeys(wizardConfig),
    );
    const idempotencyKey = joinIdempotencyKey(currentStackId);
    try {
      const run = await createWizardRun(request, idempotencyKey);
      sessionStorage.removeItem(joinIdempotencyStorageKey(currentStackId));
      return {
        jobId: run.pairing_job_id || "",
        pairingHandoff: {
          token: "",
          expiresAt: "",
          startedAt: new Date().toISOString(),
          expectedDeviceName: suggestedDeviceName(wizardConfig),
          existingServerIds: existingServerBaselineKnown
            ? existingStackServerIds
            : undefined,
        },
      };
    } catch (err) {
      if (isWizardJoinFallback(err)) {
        return null;
      }
      if (isWizardIdempotencyConflict(err)) {
        // The key already completed a different join; a retry must mint a
        // fresh key instead of replaying the dead one.
        sessionStorage.removeItem(joinIdempotencyStorageKey(currentStackId));
      }
      throw err;
    }
  }

  function isWizardIdempotencyConflict(err: unknown): boolean {
    if (!(err instanceof ApiRequestError) || err.status !== 409) return false;
    const details = err.details as Record<string, unknown> | undefined;
    const nested = details?.details as Record<string, unknown> | undefined;
    const reason = String(details?.reason_code ?? nested?.reason_code ?? "");
    return (
      reason === "wizard_idempotency_conflict" ||
      reason === "idempotency_conflict"
    );
  }

  async function prepareRegistration(wizardConfig: StackConfig = config) {
    const currentStackId = stackId;
    if (!currentStackId) {
      error = "Stack not found.";
      return;
    }
    preparing = true;
    error = null;
    let managedRuntimeAttempt: ManagedRuntimeIdempotencyAttempt | undefined;
    let managedRuntimeNeedsReplay = false;
    try {
      let jobId = "";
      let pairingHandoff: PairingHandoff | undefined;
      if (wizardConfig.serverProvisioning.mode === "kombify-cloud") {
        if (requiresProviderReselection) {
          throw new Error(
            "Select Centron or IONOS explicitly before adding a managed server to this historical stack.",
          );
        }
        const providerId = reselectedProviderId ?? wizardConfig.providerId;
        const managedRuntimeRequest: AddManagedRuntimeServerRequest = {
          node_role: wizardConfig.serverProvisioning.nodeRole,
          runtime_offering_id: wizardConfig.runtimeOfferingId,
          provider_id: providerId,
          ionos_datacenter:
            providerId === "ionos" ? wizardConfig.ionosDatacenter : undefined,
          provider_region:
            providerId === "ionos" ? wizardConfig.ionosDatacenter : undefined,
          stackkit:
            wizardConfig.serverProvisioning.stackkitFoundation ||
            wizardConfig.kit,
          services: selectedServiceKeys(wizardConfig),
        };
        managedRuntimeAttempt = getOrCreateManagedRuntimeIdempotency(
          sessionStorage,
          currentStackId,
          managedRuntimeRequest,
        );
        const managedRuntimeResult = await addManagedRuntimeServer(
          currentStackId,
          managedRuntimeRequest,
          managedRuntimeAttempt.key,
        );
        jobId = managedRuntimeResult.job_id || "";
        managedRuntimeNeedsReplay =
          (managedRuntimeResult.warnings?.length ?? 0) > 0;
      } else {
        if ($isNativeV2WizardEnabled) {
          const wizardRun = await prepareViaWizardRun(
            wizardConfig,
            currentStackId,
          );
          if (wizardRun) {
            jobId = wizardRun.jobId;
            pairingHandoff = wizardRun.pairingHandoff;
          }
        }
        if (!jobId) {
          const legacy = await prepareLegacyPairing(
            wizardConfig,
            currentStackId,
          );
          jobId = legacy.jobId;
          pairingHandoff = legacy.pairingHandoff;
        }
      }
      if (!jobId) {
        throw new Error("Server registration did not return a creation job.");
      }
      openCreationScreen(jobId, currentStackId, wizardConfig, pairingHandoff);
      if (managedRuntimeAttempt) {
        settleManagedRuntimeIdempotency(sessionStorage, managedRuntimeAttempt, {
          status: 202,
          retryable: managedRuntimeNeedsReplay,
        });
      }
    } catch (err) {
      const parsed = parseApiError(err);
      if (managedRuntimeAttempt) {
        settleManagedRuntimeIdempotency(sessionStorage, managedRuntimeAttempt, {
          status: parsed.status,
          retryable:
            parsed.outcome?.retryable ?? retryableErrorDetail(parsed.details),
        });
      }
      error = parsed.message || "Failed to prepare server registration.";
    } finally {
      preparing = false;
    }
  }

  async function prepareLegacyPairing(
    wizardConfig: StackConfig,
    currentStackId: string,
  ): Promise<{ jobId: string; pairingHandoff: PairingHandoff }> {
    const expectedDeviceName = suggestedDeviceName(wizardConfig);
    // The managed lane never reaches this function; narrow the type for the
    // pairing request contract.
    const pairingMode =
      wizardConfig.serverProvisioning.mode === "connect-remote"
        ? "connect-remote"
        : "install-command";
    const pairing = await createPairingToken(expectedDeviceName, {
      stackId: currentStackId,
      serverProvisioningMode: pairingMode,
      nodeRole: wizardConfig.serverProvisioning.nodeRole,
      stackkit:
        wizardConfig.serverProvisioning.stackkitFoundation || wizardConfig.kit,
      services: selectedServiceKeys(wizardConfig),
      remoteHost: wizardConfig.serverProvisioning.remote.host,
      remotePort: wizardConfig.serverProvisioning.remote.sshPort,
      remoteUser: wizardConfig.serverProvisioning.remote.sshUser,
      remoteAuthMethod: wizardConfig.serverProvisioning.remote.authMethod,
      remoteSSHKeyLabel: wizardConfig.serverProvisioning.remote.sshKeyLabel,
      remoteUseSudo: wizardConfig.serverProvisioning.remote.useSudo,
    });
    return {
      jobId: pairing.job_id || "",
      pairingHandoff: {
        token: pairing.token,
        expiresAt: pairing.expires_at,
        startedAt: new Date().toISOString(),
        expectedDeviceName,
        existingServerIds: existingServerBaselineKnown
          ? existingStackServerIds
          : undefined,
      },
    };
  }

  function backHref(): string {
    return stackId ? `/stacks?stack=${encodeURIComponent(stackId)}` : "/stacks";
  }

  function openCreationScreen(
    jobId: string,
    currentStackId: string,
    source: StackConfig,
    pairing?: PairingHandoff,
  ) {
    const stackName = stack?.name || source.name || "TechStack";
    const storedConfig = {
      ...JSON.parse(JSON.stringify(source)),
      creationOperation: "add-server",
    };

    sessionStorage.setItem("creatingOperation", "add-server");
    sessionStorage.setItem("creatingJobId", jobId);
    sessionStorage.setItem("creatingStackId", currentStackId);
    sessionStorage.setItem("creatingStackName", stackName);
    sessionStorage.setItem("creatingStackConfig", JSON.stringify(storedConfig));
    sessionStorage.setItem(`creating:${jobId}:operation`, "add-server");
    sessionStorage.setItem(`creating:${jobId}:stackId`, currentStackId);
    sessionStorage.setItem(`creating:${jobId}:stackName`, stackName);
    sessionStorage.setItem(
      `creating:${jobId}:config`,
      JSON.stringify(storedConfig),
    );
    if (pairing) {
      // Wizard-run joins carry no synchronous token; the creating page reads
      // it from the pairing job's result instead.
      if (pairing.token) {
        sessionStorage.setItem(
          `creating:${jobId}:registrationToken`,
          pairing.token,
        );
      }
      if (pairing.expiresAt) {
        sessionStorage.setItem(
          `creating:${jobId}:tokenExpiresAt`,
          pairing.expiresAt,
        );
      }
      sessionStorage.setItem(
        `creating:${jobId}:pairingStartedAt`,
        pairing.startedAt,
      );
      sessionStorage.setItem(
        `creating:${jobId}:expectedDeviceName`,
        pairing.expectedDeviceName,
      );
      if (pairing.existingServerIds) {
        sessionStorage.setItem(
          `creating:${jobId}:existingServerIds`,
          JSON.stringify(pairing.existingServerIds),
        );
      }
    }

    const params = new URLSearchParams({
      operation: "add-server",
      job_id: jobId,
      stack_id: currentStackId,
      name: stackName,
    });
    goto(`/stacks/creating?${params.toString()}`);
  }
</script>

<svelte:head>
  <title>Add Server | kombify-TechStack</title>
</svelte:head>

<div class="mx-auto max-w-6xl p-6 md:p-8" data-testid="add-server-page">
  <button class="btn btn-ghost mb-6" onclick={() => goto(backHref())}>
    <ArrowLeft class="h-4 w-4" />
    Back to stack
  </button>

  <div
    class="mb-4 flex flex-col gap-3 md:flex-row md:items-center md:justify-between"
  >
    <div>
      <div class="mb-2 flex items-center gap-2">
        <Server class="h-5 w-5 text-primary" />
        <span class="badge badge-secondary">Easy Wizard</span>
      </div>
      <h1 class="text-xl font-semibold text-foreground">Add Server</h1>
      <p class="max-w-3xl text-sm text-muted-foreground">
        Create or register another server for {stack?.name || "this TechStack"}
        through the same Creation Wizard.
      </p>
    </div>
  </div>

  {#if loading}
    <div class="rounded-lg border border-border bg-card p-6">
      <div class="h-5 w-48 animate-pulse rounded bg-muted"></div>
      <div class="mt-4 h-24 animate-pulse rounded bg-muted/60"></div>
    </div>
  {:else if error && !stack}
    <div
      class="rounded-lg border border-destructive/30 bg-destructive/10 p-4 text-destructive"
    >
      {error}
    </div>
  {:else}
    {#if error}
      <div
        class="mb-4 rounded-lg border border-destructive/30 bg-destructive/10 p-4 text-destructive"
      >
        {error}
      </div>
    {/if}

    {#if requiresProviderReselection}
      <div
        class="mb-4 rounded-lg border border-warning/30 bg-warning/10 p-4"
        data-testid="managed-provider-reselection-required"
      >
        <p class="text-sm text-foreground">
          This stack only has a historical provider label. Explicitly select
          Centron or IONOS in the managed-provider selector below before
          creating another server.
        </p>
      </div>
    {/if}

    <section>
      <EasyWizard
        initialConfig={config}
        oncreate={prepareRegistration}
        isDeploying={preparing}
        submitLabel="Add server"
        submittingLabel="Preparing server..."
        showServerRole={true}
        showServerServices={true}
        applyDeploymentLaneDefaults={false}
        reuseExistingOwner={true}
        onmanagedproviderselect={(providerId) => {
          reselectedProviderId = providerId;
          requiresProviderReselection = false;
        }}
      />
    </section>
  {/if}
</div>
