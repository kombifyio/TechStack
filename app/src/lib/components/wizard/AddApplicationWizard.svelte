<!--
  Add an application to an existing Homelab through the Creation Wizard's
  five modules. Only Goals (which application) and Server (which Node) can
  change here; Access, Users and Login stay inactive with their reason because
  a new application inherits them from the Homelab. Create calls the real
  registry attach or import and shows the service the backend returned.
-->
<script lang="ts">
  import { Check, PackagePlus, Server as ServerIcon } from "@lucide/svelte";
  import { EntityCard } from "@kombiverselabs/ui/entity";
  import Modal from "#lib/components/Modal.svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import Stepper from "./Stepper.svelte";
  import { tr, stateLabel } from "#lib/i18n.svelte.js";
  import { EASY_STEPS } from "#lib/wizard/index.js";
  import { operationStepPlan } from "#lib/wizard/operation-steps.js";
  import {
    attachCatalogService,
    importObservedService,
    type RegistryCatalogService,
    type RegistryServer,
    type RegistryService,
    type ServiceRegistryPayload,
  } from "#lib/api/registry.js";
  import {
    serviceCardLogoDomain,
    serviceCardLogoSrc,
  } from "#lib/service-card-adapter.js";

  interface Props {
    registry: ServiceRegistryPayload;
    /** Node preselected from the page, e.g. the one the Placement board shows. */
    initialServerId?: string;
    onclose: () => void;
    onadded: (service: RegistryService) => void;
  }

  let { registry, initialServerId = "", onclose, onadded }: Props = $props();

  const steps = EASY_STEPS;
  const inactiveReasons: Record<number, string> = {
    3: tr("wizard.step.inherited"),
    4: tr("wizard.step.inherited"),
    5: tr("wizard.step.existingOwner"),
  };
  const plan = operationStepPlan(steps, inactiveReasons);

  let step = $state(plan.first);
  let mode = $state<"catalog" | "import">("catalog");
  let catalogId = $state("");
  // svelte-ignore state_referenced_locally
  let serverId = $state(initialServerId);
  let importName = $state("");
  let importDisplayName = $state("");
  let importType = $state("custom");
  let importPort = $state("");
  let importUrl = $state("");
  let saving = $state(false);
  let error = $state<string | null>(null);
  let added = $state<RegistryService | null>(null);

  const selectedApp = $derived(
    registry.catalog.find((service) => service.id === catalogId),
  );
  const chosenName = $derived(
    mode === "catalog"
      ? (selectedApp?.display_name ?? "")
      : importDisplayName.trim() || importName.trim(),
  );

  function foundationOf(server: RegistryServer): string | undefined {
    return registry.kit_deployments.find(
      (deployment) => deployment.id === server.kit_deployment_id,
    )?.stackkit_foundation;
  }

  /** Why a Node cannot take the chosen application, or "" when it can. */
  function nodeBlockedReason(server: RegistryServer): string {
    if (!server.rollout_ready) return tr("wizard.addApp.nodeNotReady");
    if (mode !== "catalog" || !selectedApp) return "";
    const foundation = foundationOf(server);
    if (
      selectedApp.foundations.length > 0 &&
      foundation &&
      !selectedApp.foundations.includes(foundation)
    ) {
      return tr("wizard.addApp.nodeUnsupported", {
        name: selectedApp.display_name,
      });
    }
    return "";
  }

  const selectedServer = $derived(
    registry.servers.find((server) => server.id === serverId),
  );

  // Keep the Node choice on a Node that can take the application.
  $effect(() => {
    if (selectedServer && !nodeBlockedReason(selectedServer)) return;
    serverId =
      registry.servers.find((server) => !nodeBlockedReason(server))?.id ?? "";
  });

  const canGoNext = $derived(
    step === 1
      ? mode === "catalog"
        ? Boolean(selectedApp)
        : importName.trim().length > 0
      : true,
  );
  const canCreate = $derived(
    canGoNext &&
      Boolean(selectedServer) &&
      !nodeBlockedReason(selectedServer!) &&
      !saving,
  );

  function next() {
    const target = plan.next(step);
    if (target && canGoNext) step = target;
  }

  function previous() {
    const target = plan.previous(step);
    if (target) step = target;
  }

  async function create() {
    if (!selectedServer || !canCreate) return;
    saving = true;
    error = null;
    try {
      const service =
        mode === "catalog"
          ? await attachCatalogService({
              kit_deployment_id: selectedServer.kit_deployment_id,
              server_id: selectedServer.id,
              service_id: catalogId,
            })
          : await importObservedService({
              kit_deployment_id: selectedServer.kit_deployment_id,
              server_id: selectedServer.id,
              name: importName,
              display_name: importDisplayName,
              type: importType,
              port: importPort ? Number(importPort) : undefined,
              url: importUrl,
            });
      added = service;
      onadded(service);
    } catch (err) {
      error =
        err instanceof Error
          ? err.message
          : tr(
              mode === "catalog"
                ? "ui.services.catalogServiceCouldNotBe"
                : "ui.services.unmanagedServiceCouldNotBe",
            );
    } finally {
      saving = false;
    }
  }

  function close() {
    if (!saving) onclose();
  }

  function logoFor(service: RegistryCatalogService): string | undefined {
    return serviceCardLogoSrc(
      serviceCardLogoDomain(null, service.id, service.display_name),
    );
  }

  function typeLabel(type: string): string {
    return type.replace(/[-_]/g, " ");
  }
</script>

<Modal title={tr("wizard.addApp.title")} onClose={close} maxWidth="3xl">
  <div data-testid="new-service-dialog" class="min-w-0">
    {#if added}
      <div
        class="flex flex-col items-start gap-3 py-2"
        role="status"
        data-testid="add-application-result"
      >
        <span
          class="flex h-10 w-10 items-center justify-center rounded-full bg-success/15 text-success"
          ><Check class="h-5 w-5" aria-hidden="true" /></span
        >
        <p class="text-base font-semibold text-foreground">
          {tr("wizard.addApp.result", {
            name: added.application_name || added.display_name,
            node: added.server_name || selectedServer?.name || "",
          })}
        </p>
        <p class="text-sm text-muted-foreground">
          {tr("wizard.addApp.resultStatus", {
            status: stateLabel(added.status),
          })}
        </p>
        <Button variant="primary" onclick={onclose} testId="add-application-done">
          {tr("wizard.addApp.done")}
        </Button>
      </div>
    {:else}
      <p class="mb-4 text-sm text-muted-foreground">
        {tr("wizard.addApp.intro")}
      </p>
      <Stepper
        {steps}
        disabledStepReasons={inactiveReasons}
        currentStep={step}
        {canGoNext}
        canDeploy={canCreate}
        isDeploying={saving}
        deployLabel={mode === "catalog"
          ? tr("wizard.addApp.submit")
          : tr("ui.services.importAsObserved")}
        deployingLabel={mode === "catalog"
          ? tr("ui.serviceDiscovery.adding")
          : tr("ui.importExportModal.importing")}
        onprev={previous}
        onnext={next}
        ondeploy={create}
      />

      {#if registry.servers.length === 0}
        <div
          class="rounded-lg border border-warning/40 bg-warning/10 p-4 text-sm text-warning"
          role="status"
        >
          {tr("ui.services.aRegisteredNodeIsRequired")}
        </div>
      {:else if step === 1}
        <section data-testid="add-application-goals">
          <div
            class="mb-4 flex gap-2"
            role="tablist"
            aria-label={tr("ui.services.serviceMode")}
          >
            <Button
              variant={mode === "catalog" ? "primary" : "secondary"}
              onclick={() => (mode = "catalog")}
            >
              {tr("ui.services.catalog")}
            </Button>
            <Button
              variant={mode === "import" ? "primary" : "secondary"}
              onclick={() => (mode = "import")}
            >
              {tr("ui.services.importUnmanaged")}
            </Button>
          </div>

          {#if mode === "catalog"}
            {#if registry.catalog.length === 0}
              <p class="text-sm text-muted-foreground">
                {tr("wizard.addApp.catalogEmpty")}
              </p>
            {:else}
              <div class="grid gap-3 sm:grid-cols-2">
                {#each registry.catalog as service (service.id)}
                  <div data-testid={`catalog-service-${service.id}`}>
                    <EntityCard
                      kind="application"
                      size="tile"
                      name={service.display_name}
                      meta={typeLabel(service.type)}
                      imageUrl={logoFor(service)}
                      icon={PackagePlus}
                      presence="unknown"
                      statusLabel={service.recommended
                        ? tr("wizard.addApp.recommended")
                        : typeLabel(service.type)}
                      facts={[
                        {
                          label: tr("wizard.addApp.about"),
                          value: service.description,
                        },
                      ]}
                      selected={catalogId === service.id}
                      onselect={() => (catalogId = service.id)}
                    />
                  </div>
                {/each}
              </div>
            {/if}
          {:else}
            <div class="grid gap-4 md:grid-cols-2">
              <label class="block">
                <span class="mb-1 block text-sm text-muted-foreground"
                  >{tr("ui.credentialForm.name")}</span
                >
                <input
                  bind:value={importName}
                  class="w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground"
                  placeholder="custom-dashboard"
                  data-testid="unmanaged-service-name"
                />
              </label>
              <label class="block">
                <span class="mb-1 block text-sm text-muted-foreground"
                  >{tr("ui.services.displayName")}</span
                >
                <input
                  bind:value={importDisplayName}
                  class="w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground"
                  placeholder="Custom Dashboard"
                />
              </label>
              <label class="block">
                <span class="mb-1 block text-sm text-muted-foreground"
                  >{tr("ui.services.type2")}</span
                >
                <input
                  bind:value={importType}
                  class="w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground"
                  placeholder="custom"
                />
              </label>
              <label class="block">
                <span class="mb-1 block text-sm text-muted-foreground"
                  >{tr("ui.services.port")}</span
                >
                <input
                  type="number"
                  min="1"
                  max="65535"
                  bind:value={importPort}
                  class="w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground"
                  placeholder="8080"
                />
              </label>
              <label class="block md:col-span-2">
                <span class="mb-1 block text-sm text-muted-foreground"
                  >{tr("ui.services.url")}</span
                >
                <input
                  type="url"
                  bind:value={importUrl}
                  class="w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground"
                  placeholder="http://server.local:8080"
                />
              </label>
            </div>
          {/if}
        </section>
      {:else if step === 2}
        <section data-testid="add-application-server">
          <h3 class="mb-3 text-sm font-semibold text-foreground">
            {tr("wizard.addApp.nodeTitle", { name: chosenName })}
          </h3>
          <div class="grid gap-3 sm:grid-cols-2" data-testid="service-target-server">
            {#each registry.servers as server (server.id)}
              {@const blocked = nodeBlockedReason(server)}
              <div
                data-testid={`add-application-node-${server.id}`}
                class={blocked ? "opacity-55" : ""}
                title={blocked || undefined}
              >
                <EntityCard
                  kind="server"
                  size="tile"
                  name={server.name}
                  meta={server.hostname || server.role_label}
                  icon={ServerIcon}
                  presence={server.health_state === "healthy" ||
                  server.status === "healthy"
                    ? "online"
                    : server.health_state === "offline"
                      ? "offline"
                      : "unknown"}
                  statusLabel={blocked || server.role_label}
                  selected={serverId === server.id}
                  onselect={blocked ? undefined : () => (serverId = server.id)}
                />
              </div>
            {/each}
          </div>
        </section>
      {/if}

      {#if error}
        <div
          class="mt-5 rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive"
          role="alert"
        >
          {error}
        </div>
      {/if}
    {/if}
  </div>
</Modal>
