<script lang="ts">
  import ErrorCallout from "#lib/components/ui/ErrorCallout.svelte";
  import LoadingNote from "#lib/components/ui/LoadingNote.svelte";
  import { goto as svelteGoto } from "$app/navigation";
  import { onMount } from "svelte";
  import { getKitDeployment, type KitDeployment } from "#lib/api/stacks.js";
  import type { RegistryKitDeployment } from "#lib/api/registry.js";
  import { parseApiError } from "#lib/api/errors.js";
  import type { LayoutRecommendationAcceptance } from "#lib/api/layout.js";
  import { isWizardIdempotencyConflict } from "#lib/wizard/wizard-conflict-recovery.js";
  import { CREATION_PROCESSES } from "#lib/creation/core/index.js";
  import {
    creationScreenHref,
    creationScreenHrefForConflict,
    forgetCreationAttempt,
    submitCreationRun,
  } from "#lib/creation/submit.js";
  import SubstrateEnrollment from "./SubstrateEnrollment.svelte";
  import InfoTip from "#lib/components/wizard/InfoTip.svelte";
  import { NODE_GUIDES } from "#lib/docs-links.js";
  import { tr } from "#lib/i18n.svelte.js";
  import {
    applyServerProvisioningMode,
    createExpansionConfig,
    normalizeIonosDatacenter,
    readRuntimeOfferingID,
    type StackConfig,
  } from "#lib/wizard/index.js";
  import { CreationPageFrame } from "#lib/components/wizard/index.js";

  interface Props {
    stackId: string;
    /** Remove page-owned chrome and spacing when mounted by a Home Hub host. */
    embedded?: boolean;
    onNavigate?: (href: string) => void | Promise<void>;
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

  async function navigate(href: string) {
    if (onNavigate) {
      await onNavigate(href);
      return;
    }
    await svelteGoto(href);
  }

  let config = $state<StackConfig>(createExpansionConfig());
  let additionMode = $state<"join" | "found" | "substrate">("join");
  const process = $derived(
    additionMode === "found"
      ? CREATION_PROCESSES["add-server-found"]
      : CREATION_PROCESSES["add-server"],
  );

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
  let submitError = $state<string | null>(null);
  let requiresProviderReselection = $state(false);
  let offeringReadbackFailed = $state(false);
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
      try {
        config.runtimeOfferingId = readRuntimeOfferingID(
          deployment.runtime_offering_id,
        );
      } catch {
        offeringReadbackFailed = true;
        error = tr("wizard.server.managed.specsUnavailable");
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

  async function prepareRegistration(
    wizardConfig: StackConfig = config,
    extras?: { layoutRecommendation?: LayoutRecommendationAcceptance },
  ) {
    const target = {
      stackId,
      stackName: deployment?.name || "",
      layoutRecommendation: extras?.layoutRecommendation,
    };
    if (!stackId) {
      error = tr("ui.managedCreationFlow.stackkitDeploymentNotFound");
      return;
    }
    preparing = true;
    submitError = null;
    try {
      if (wizardConfig.serverProvisioning.mode === "kombify-cloud") {
        if (offeringReadbackFailed) {
          throw new Error(tr("wizard.server.managed.specsUnavailable"));
        }
        if (requiresProviderReselection) {
          throw new Error(
            tr("ui.managedCreationFlow.selectCentronOrIonosExplicitly"),
          );
        }
      }
      const { run } = await submitCreationRun(process, wizardConfig, target);
      if (!run.job_id && !run.pairing_job_id) {
        throw new Error(
          tr("ui.managedCreationFlow.failedToPrepareNodeRegistration"),
        );
      }
      await navigate(
        creationScreenHref(
          process,
          run,
          run.name || deployment?.name || wizardConfig.name || "Techstack",
        ),
      );
    } catch (err) {
      // The server already knows this attempt: its run is followed, not repeated.
      if (isWizardIdempotencyConflict(err)) {
        forgetCreationAttempt(process, target);
        const screen = await creationScreenHrefForConflict(
          process,
          err,
          target,
        );
        if (screen) {
          await navigate(screen);
        } else {
          submitError = tr(
            "ui.stacksCreatingCreationFailure.earlierRegistrationAttemptAlreadyCompleted",
          );
        }
        return;
      }
      const parsed = parseApiError(err);
      submitError =
        parsed.message ||
        tr("ui.managedCreationFlow.failedToPrepareNodeRegistration");
    } finally {
      preparing = false;
    }
  }
</script>

<div
  class={embedded ? "w-full p-6" : "mx-auto max-w-6xl p-6 md:p-8"}
  data-testid="add-server-page"
  data-flow="managed-creation"
  data-presentation={embedded ? "embedded" : "page"}
>

  {#if loading}
    <LoadingNote label={tr("common.loading")} />
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
    <CreationPageFrame
      {process}
      initialConfig={config}
      oncreate={prepareRegistration}
      isDeploying={preparing}
      submitLabel={tr("ui.managedCreationFlow.addNode")}
      submittingLabel={tr("ui.managedCreationFlow.preparingNode")}
      layoutStackId={stackId}
      nodeAlternative={additionMode === "substrate"
        ? substrateContent
        : undefined}
      onmanagedproviderselect={(providerId) => {
        config.providerId = providerId;
        requiresProviderReselection = false;
      }}
    >
      {#snippet footerNotice()}
        {#if submitError}
          <ErrorCallout message={submitError} testId="add-server-notice" />
        {/if}
      {/snippet}
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
    </CreationPageFrame>
  {/if}
</div>
