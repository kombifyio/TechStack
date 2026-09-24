<script lang="ts">
  import { goto as svelteGoto } from "$app/navigation";
  import { onMount } from "svelte";
  import { getKitDeployment, type KitDeployment } from "#lib/api/stacks.js";
  import type { RegistryKitDeployment } from "#lib/api/registry.js";
  import { parseApiError } from "#lib/api/errors.js";
  import {
    mintJoinWizardIdempotencyKey,
    type WizardJoinAdditionMode,
  } from "#lib/wizard/idempotency-keys.js";
  import type { WizardRunRequest } from "#lib/api/wizardRuns.js";
  import {
    buildFoundRunRequest,
    buildJoinRunRequest,
    wizardRunRequestCarriesSecret,
  } from "#lib/wizard/wizardRunRequest.js";
  import SubstrateEnrollment from "./SubstrateEnrollment.svelte";
  import {
    applyServerProvisioningMode,
    CANONICAL_USE_CASE_GOALS,
    createDefaultConfig,
    normalizeIonosDatacenter,
    type StackConfig,
  } from "#lib/wizard/index.js";
  import { EasyWizard } from "#lib/components/wizard/index.js";
  import { ArrowLeft, Server } from "@lucide/svelte";

  type NavigateOptions = Parameters<typeof svelteGoto>[1];

  interface Props {
    stackId: string;
    /** Remove page-owned chrome and spacing when mounted by a Home Hub host. */
    embedded?: boolean;
    backTo?: string;
    onNavigate?: (
      href: string,
      options?: NavigateOptions,
    ) => void | Promise<void>;
  }

  let {
    stackId,
    embedded = false,
    backTo = "/dashboard",
    onNavigate,
  }: Props = $props();

  async function navigate(href: string, options?: NavigateOptions) {
    if (onNavigate) {
      await onNavigate(href, options);
      return;
    }
    await svelteGoto(href, options);
  }

  function createAddServerConfig(): StackConfig {
    const next = createDefaultConfig();
    // The first-run defaults carry a pre-selected use case (vault). On an
    // Additional Node that selection was never made by anyone, and the
    // backend merges an expansion's goals into the homelab backlog, so a
    // default riding along here would silently add a use case the operator
    // did not ask for. Start empty; what is sent is what was picked.
    if (next.goals) {
      for (const goal of CANONICAL_USE_CASE_GOALS) {
        next.goals[goal] = false;
      }
      next.goals.everything = false;
    }
    next.serverProvisioning.nodeRole = "worker";
    next.owner.bootstrapMode = "none";
    next.auth.requirePassword = false;
    next.auth.requireMfa = false;
    next.auth.allowPasswordless = false;
    return next;
  }

  let config = $state<StackConfig>(createAddServerConfig());
  let additionMode = $state<"join" | "found" | "substrate">("join");

  function chooseAddition(
    mode: typeof additionMode,
    wizardConfig: StackConfig,
  ) {
    additionMode = mode;
    if (mode === "join" && deployment?.stackkit_foundation) {
      wizardConfig.kit = deployment.stackkit_foundation as StackConfig["kit"];
      wizardConfig.serverProvisioning.stackkitFoundation = wizardConfig.kit;
    }
    if (mode !== "substrate") {
      wizardConfig.serverProvisioning.nodeRole =
        mode === "found" ? "foundation" : "worker";
    }
  }
  let deployment = $state<RegistryKitDeployment | null>(null);
  let loading = $state(true);
  let preparing = $state(false);
  let error = $state<string | null>(null);
  let requiresProviderReselection = $state(false);
  let reselectedProviderId = $state<"centron" | "ionos" | null>(null);
  let stackDefaultsApplied = false;

  onMount(() => {
    void load();
  });

  $effect(() => {
    if (!deployment || stackDefaultsApplied) return;
    stackDefaultsApplied = true;
    if (deployment.stackkit_foundation) {
      config.serverProvisioning.stackkitFoundation =
        deployment.stackkit_foundation as StackConfig["kit"];
      config.kit = deployment.stackkit_foundation as StackConfig["kit"];
    }
    if (isManagedRuntimeDeployment(deployment)) {
      applyServerProvisioningMode(config, "kombify-cloud");
      if (
        deployment.provider_id === "centron" ||
        deployment.provider_id === "ionos"
      ) {
        config.providerId = deployment.provider_id;
        reselectedProviderId = null;
        requiresProviderReselection = false;
      } else {
        // Historical provider labels are display-only and must never become a
        // fresh executable selection. The user must choose a canonical ID.
        requiresProviderReselection = true;
      }
      config.ionosDatacenter = normalizeIonosDatacenter(
        deployment.ionos_datacenter || deployment.provider_region,
      );
      if (
        deployment.runtime_offering_id === "monthly-runtime-standard" ||
        deployment.runtime_offering_id === "monthly-runtime-premium"
      ) {
        config.runtimeOfferingId = deployment.runtime_offering_id;
      }
    }
  });

  async function load() {
    loading = true;
    error = null;
    try {
      deployment = await loadDeploymentFallback();
      if (!deployment) {
        error = "StackKit deployment not found.";
      }
    } catch (err) {
      deployment = await loadDeploymentFallback();
      if (!deployment) {
        const parsed = parseApiError(err);
        error = parsed.message || "Failed to load Node inventory.";
      }
    } finally {
      loading = false;
    }
  }

  async function loadDeploymentFallback(): Promise<RegistryKitDeployment | null> {
    if (!stackId) return null;
    try {
      return kitDeploymentToRegistryDeployment(await getKitDeployment(stackId));
    } catch {
      return null;
    }
  }

  function kitDeploymentToRegistryDeployment(
    item: KitDeployment,
  ): RegistryKitDeployment {
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

  function isManagedRuntimeDeployment(item: RegistryKitDeployment): boolean {
    return (
      item.server_provisioning_mode === "kombify-cloud" ||
      item.server_mode === "monthly-runtime" ||
      item.server_mode === "managed-cloud" ||
      item.runtime_lane === "monthly-runtime"
    );
  }

  function selectedServiceKeys(source: StackConfig): string[] {
    return Object.entries(source.services)
      .filter(([, enabled]) => enabled)
      .map(([name]) => name);
  }

  function joinIdempotencyKey(
    id: string,
    mode: WizardJoinAdditionMode = "join",
  ): string {
    return mintJoinWizardIdempotencyKey(id, mode);
  }

  async function prepareRegistration(wizardConfig: StackConfig = config) {
    const currentStackId = stackId;
    if (!currentStackId) {
      error = "StackKit deployment not found.";
      return;
    }
    preparing = true;
    error = null;
    try {
      if (wizardConfig.serverProvisioning.mode === "kombify-cloud") {
        if (requiresProviderReselection) {
          throw new Error(
            "Select Centron or IONOS explicitly before adding a managed Node to this historical StackKit deployment.",
          );
        }
        const providerId = reselectedProviderId ?? wizardConfig.providerId;
        if (providerId !== wizardConfig.providerId) {
          wizardConfig = { ...wizardConfig, providerId };
        }
      }
      const request =
        additionMode === "found"
          ? buildFoundRunRequest(wizardConfig)
          : buildJoinRunRequest(
              wizardConfig,
              currentStackId,
              deployment?.name || "",
              selectedServiceKeys(wizardConfig),
            );
      await openCreationScreen(
        request,
        joinIdempotencyKey(
          currentStackId,
          additionMode === "found" ? "found" : "join",
        ),
        currentStackId,
        wizardConfig,
      );
    } catch (err) {
      const parsed = parseApiError(err);
      error = parsed.message || "Failed to prepare Node registration.";
    } finally {
      preparing = false;
    }
  }

  async function openCreationScreen(
    request: WizardRunRequest,
    idempotencyKey: string,
    currentStackId: string,
    source: StackConfig,
  ) {
    const stackName = deployment?.name || source.name || "Techstack";

    const params = new URLSearchParams({
      name: stackName,
    });
    if (additionMode === "join") {
      params.set("operation", "add-server");
      params.set("stack_id", currentStackId);
    }
    await navigate(`/stacks/creating?${params.toString()}`, {
      state: {
        wizardRunSubmission: { request, idempotencyKey },
      },
      // The handoff survives a reload until the server returns a durable job,
      // but only when it carries no credential: an SSH password stays in
      // memory for client-side navigation and is never written to
      // sessionStorage. Reloading such a request requires re-entering the
      // password in the wizard.
      persistState: !wizardRunRequestCarriesSecret(request),
    });
  }
</script>

<div
  class={embedded ? "w-full" : "mx-auto max-w-6xl p-6 md:p-8"}
  data-testid="add-server-page"
  data-flow="managed-creation"
  data-presentation={embedded ? "embedded" : "page"}
>
  {#if !embedded}
    <button
      data-kx="control"
      class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50 mb-6"
      onclick={() => void navigate(backTo)}
    >
      <ArrowLeft class="h-4 w-4" />
      Back to StackKit deployment
    </button>
  {/if}

  <div
    class="mb-4 flex flex-col gap-3 md:flex-row md:items-center md:justify-between"
  >
    <div>
      <div class="mb-2 flex items-center gap-2">
        <Server class="h-5 w-5 text-primary" />
      </div>
      <h1 class="text-xl font-semibold text-foreground">Add Node</h1>
      <p class="max-w-3xl text-sm text-muted-foreground">
        Add another device to your Homelab. We will guide you through the setup.
      </p>
    </div>
  </div>

  {#if loading}
    <div class="rounded-lg border border-border bg-card p-6">
      <div class="h-5 w-48 animate-pulse rounded bg-muted"></div>
      <div class="mt-4 h-24 animate-pulse rounded bg-muted/60"></div>
    </div>
  {:else if error && !deployment}
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

    {#snippet substrateContent()}
      <SubstrateEnrollment deploymentId={stackId} />
    {/snippet}
    <EasyWizard
      initialConfig={config}
      oncreate={prepareRegistration}
      isDeploying={preparing}
      submitLabel="Add Node"
      submittingLabel="Preparing Node..."
      showServerRole={true}
      showServerServices={false}
      applyDeploymentLaneDefaults={false}
      reuseExistingOwner={true}
      joinExistingDeployment={additionMode !== "found"}
      nodeAlternative={additionMode === "substrate"
        ? substrateContent
        : undefined}
      onmanagedproviderselect={(providerId) => {
        reselectedProviderId = providerId;
        requiresProviderReselection = false;
      }}
    >
      {#snippet nodeOptions(wizardConfig)}
        <fieldset class="mb-6 flex flex-wrap gap-3">
          <legend class="mb-2 font-medium">Node configuration</legend>
          {#each [{ value: "join", label: "Worker or storage for this StackKit" }, { value: "found", label: "New StackKit / main Node" }, { value: "substrate", label: "Proxmox hypervisor" }] as option}
            <button
              type="button"
              class="rounded-lg border border-border px-4 py-3 aria-pressed:bg-muted"
              aria-pressed={additionMode === option.value}
              onclick={() =>
                chooseAddition(
                  option.value as typeof additionMode,
                  wizardConfig,
                )}>{option.label}</button
            >
          {/each}
        </fieldset>
        {#if additionMode !== "substrate" && requiresProviderReselection && wizardConfig.serverProvisioning.mode === "kombify-cloud"}
          <div
            class="mb-4 rounded-lg border border-warning/30 bg-warning/10 p-4"
            data-testid="managed-provider-reselection-required"
          >
            <p class="text-sm text-foreground">
              Select Centron or IONOS below before adding a managed Node. This
              StackKit deployment has no current provider selection.
            </p>
          </div>
        {/if}
      {/snippet}
    </EasyWizard>
  {/if}
</div>
