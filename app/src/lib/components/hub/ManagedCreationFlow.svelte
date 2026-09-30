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
  import InfoTip from "#lib/components/wizard/InfoTip.svelte";
  import { NODE_GUIDES } from "#lib/docs-links.js";
  import { tr } from "#lib/i18n.svelte.js";
  import {
    applyServerProvisioningMode,
    CANONICAL_USE_CASE_GOALS,
    createDefaultConfig,
    normalizeIonosDatacenter,
    type StackConfig,
  } from "#lib/wizard/index.js";
  import { EasyWizard } from "#lib/components/wizard/index.js";

  type NavigateOptions = Parameters<typeof svelteGoto>[1];

  interface Props {
    stackId: string;
    /** Remove page-owned chrome and spacing when mounted by a Home Hub host. */
    embedded?: boolean;
    onNavigate?: (
      href: string,
      options?: NavigateOptions,
    ) => void | Promise<void>;
  }

  let {
    stackId,
    embedded = false,
    onNavigate,
  }: Props = $props();

  const additionOptions = $derived([
    {
      value: "join",
      label: tr("ui.managedCreationFlow.workerOrStorageForThis"),
      tip: "wizard.tip.nodeJoin",
      docs: NODE_GUIDES.joinStackKit,
    },
    {
      value: "found",
      label: tr("ui.managedCreationFlow.newStackkitMainNode"),
      tip: "wizard.tip.nodeFound",
      docs: NODE_GUIDES.newStackKit,
    },
    {
      value: "substrate",
      label: tr("ui.managedCreationFlow.proxmoxHypervisor"),
      tip: "wizard.tip.nodeSubstrate",
      docs: NODE_GUIDES.nodeConfiguration,
    },
  ] as const);

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
        requiresProviderReselection = false;
      } else {
        // Historical provider labels are display-only. The provider step must
        // confirm an entitled canonical ID before this request can proceed.
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
        error = tr("ui.managedCreationFlow.stackkitDeploymentNotFound");
      }
    } catch (err) {
      deployment = await loadDeploymentFallback();
      if (!deployment) {
        const parsed = parseApiError(err);
        error = parsed.message || tr("ui.managedCreationFlow.failedToLoadNodeInventory");
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
      error = tr("ui.managedCreationFlow.stackkitDeploymentNotFound");
      return;
    }
    preparing = true;
    error = null;
    try {
      if (wizardConfig.serverProvisioning.mode === "kombify-cloud") {
        if (requiresProviderReselection) {
          throw new Error(
            tr("ui.managedCreationFlow.selectCentronOrIonosExplicitly"),
          );
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
      error = parsed.message || tr("ui.managedCreationFlow.failedToPrepareNodeRegistration");
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
      submitLabel={tr("ui.managedCreationFlow.addNode")}
      submittingLabel={tr("ui.managedCreationFlow.preparingNode")}
      showServerRole={true}
      showServerServices={false}
      applyDeploymentLaneDefaults={false}
      reuseExistingOwner={true}
      joinExistingDeployment={additionMode !== "found"}
      nodeAlternative={additionMode === "substrate"
        ? substrateContent
        : undefined}
      onmanagedproviderselect={(providerId) => {
        config.providerId = providerId;
        requiresProviderReselection = false;
      }}
    >
      {#snippet nodeOptions(wizardConfig)}
        <div class="mb-6" data-substep>
        <fieldset class="flex flex-wrap gap-3">
          <legend class="mb-2 font-medium">{tr("ui.managed.nodeConfiguration")}</legend>
          {#each additionOptions as option (option.value)}
            <span class="inline-flex items-center gap-0.5">
            <button
              type="button"
              class="rounded-lg border border-border px-4 py-3 aria-pressed:bg-muted"
              aria-pressed={additionMode === option.value}
              onclick={() =>
                chooseAddition(
                  option.value,
                  wizardConfig,
                )}>{option.label}</button
            ><InfoTip
              topic={option.label}
              text={tr(option.tip)}
              docs={option.docs}
            />
            </span>
          {/each}
        </fieldset>
        </div>
        {#if additionMode !== "substrate" && requiresProviderReselection && wizardConfig.serverProvisioning.mode === "kombify-cloud"}
          <div
            class="mb-4 rounded-lg border border-warning/30 bg-warning/10 p-4"
            data-testid="managed-provider-reselection-required"
          >
            <p class="text-sm text-foreground">
              {tr("ui.managed.selectProvider")}
            </p>
          </div>
        {/if}
      {/snippet}
    </EasyWizard>
  {/if}
</div>
