<script lang="ts">
  import LoadingNote from "#lib/components/ui/LoadingNote.svelte";
  import { tr, trn, formatDateTime, stateLabel } from "#lib/i18n.svelte.js";
  import { onMount, untrack } from "svelte";
  import { page } from "$app/state";
  import { goto, replaceState } from "$app/navigation";
  import {
    Activity,
    ArrowLeft,
    ExternalLink,
    Loader2,
    RefreshCw,
    Server,
  } from "@lucide/svelte";
  import { PageHeader } from "@kombiverselabs/ui/shell";
  import { ServiceCard } from "@kombiverselabs/ui/service";
  import Button from "#lib/components/ui/Button.svelte";
  import BrandLogoIcon from "#lib/components/BrandLogoIcon.svelte";
  import BrandLogoScope from "#lib/components/BrandLogoScope.svelte";
  import ServiceAdoptionDialog from "#lib/components/inventory/ServiceAdoptionDialog.svelte";
  import { listJobs, type JobDetail } from "#lib/api/jobs.js";
  import {
    listServiceRegistry,
    type ServiceRegistryPayload,
  } from "#lib/api/registry.js";
  import {
    getServiceApplication,
    type CanonicalService,
    type ServiceApplication,
  } from "#lib/api/services.js";
  import {
    openServiceUrl,
    serviceCardLogoDomain,
    serviceCardLogoSrc,
    serviceCardStatus,
    serviceCardStatusMessage,
    serviceCardUpdate,
    serviceCardVersion,
    serviceIsLocked,
    serviceLockReason,
    serviceOpenUrl,
    type GovernedServiceAction,
  } from "#lib/service-card-adapter.js";
  import {
    ServiceActionTracker,
    actionFailed,
    isActionActive,
  } from "#lib/services/service-action-tracker.svelte.js";
  import {
    actionLabel,
    applicationCardActions,
    componentCardActions,
    runApplicationAction,
    runComponentAction,
    type ApplicationAction,
  } from "#lib/services/application-actions.js";
  import {
    applicationLocked,
    applicationOwnership,
    applicationPlacement,
    applicationVersion,
    componentState,
    logsTarget,
    runtimePlacement,
  } from "#lib/services/application-card.js";

  type TabId = "overview" | "components" | "logs" | "activity" | "settings";
  const TABS: readonly TabId[] = [
    "overview",
    "components",
    "logs",
    "activity",
    "settings",
  ];

  function parseTab(value: string | null): TabId {
    return TABS.find((candidate) => candidate === value) ?? "overview";
  }

  const applicationId = $derived(page.params.applicationId ?? "");
  let application = $state<ServiceApplication | null>(null);
  let registry = $state<ServiceRegistryPayload | null>(null);
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let actionError = $state<string | null>(null);
  let tab = $state<TabId>(parseTab(page.url.searchParams.get("tab")));
  let logComponentId = $state<string>(
    page.url.searchParams.get("component") ?? "",
  );
  let adoption = $state<{ service: CanonicalService; release: boolean } | null>(
    null,
  );

  interface RecordedActivity {
    loading: boolean;
    loaded: boolean;
    error: string | null;
    jobs: JobDetail[];
  }
  let recorded = $state<RecordedActivity>({
    loading: false,
    loaded: false,
    error: null,
    jobs: [],
  });

  let mounted = false;
  const tracker = new ServiceActionTracker({
    refresh: () => reloadQuietly(),
    observedRevision: (serviceId) =>
      application?.components.find((component) => component.id === serviceId)
        ?.inventory_revision,
  });

  onMount(() => {
    mounted = true;
    tracker.start();
    void load();
    const interval = setInterval(() => {
      if (tracker.hasActiveJobs() || tracker.hasConverging()) {
        void tracker.tick();
      }
    }, 3000);
    return () => {
      mounted = false;
      tracker.stop();
      clearInterval(interval);
    };
  });

  async function load() {
    loading = true;
    loadError = null;
    try {
      application = await getServiceApplication(applicationId);
    } catch (cause) {
      loadError =
        cause instanceof Error
          ? cause.message
          : tr("ui.servicesApplicationId.applicationDetailsCouldNotBe");
    } finally {
      loading = false;
    }
    // Stack, server and provider names are context, not the page's subject:
    // the page renders without them when the registry is unreachable.
    listServiceRegistry()
      .then((payload) => {
        if (mounted) registry = payload;
      })
      .catch(() => undefined);
  }

  /** Re-read the application after a mutation without blanking the page. */
  async function reloadQuietly() {
    try {
      const next = await getServiceApplication(applicationId);
      if (mounted) application = next;
    } catch {
      // Keep the last-known-good application; the next poll tries again.
    }
    if (mounted && recorded.loaded) void loadActivity();
  }

  function setTab(next: TabId) {
    tab = next;
    const url = new URL(location.href);
    if (next === "overview") url.searchParams.delete("tab");
    else url.searchParams.set("tab", next);
    if (next !== "logs") url.searchParams.delete("component");
    replaceState(url, {});
  }

  function selectLogComponent(id: string) {
    logComponentId = id;
    const url = new URL(location.href);
    url.searchParams.set("component", id);
    replaceState(url, {});
  }

  function onTabKeydown(event: KeyboardEvent, index: number) {
    if (event.key !== "ArrowRight" && event.key !== "ArrowLeft") return;
    event.preventDefault();
    const step = event.key === "ArrowRight" ? 1 : -1;
    const next = TABS[(index + step + TABS.length) % TABS.length];
    setTab(next);
    queueMicrotask(() =>
      document.getElementById(`application-tab-${next}`)?.focus(),
    );
  }

  const placementContext = $derived({
    servers: registry?.servers ?? [],
    kitDeployments: registry?.kit_deployments ?? [],
  });
  const registryServiceFor = (runtime: CanonicalService) =>
    registry?.services.find((service) => service.id === runtime.id);

  const placement = $derived(
    application
      ? applicationPlacement(application, placementContext, registryServiceFor)
      : "unknown",
  );
  const version = $derived(
    application ? applicationVersion(application) : undefined,
  );
  const server = $derived(
    registry?.servers.find(
      (candidate) => candidate.id === application?.server_id,
    ),
  );
  const stack = $derived(
    registry?.kit_deployments.find(
      (candidate) => candidate.id === application?.kit_deployment_id,
    ),
  );
  const logoDomain = $derived(
    application
      ? serviceCardLogoDomain(
          application.catalog ?? application.components[0]?.catalog,
          application.application_key,
          application.display_name,
        )
      : "",
  );
  const status = $derived(
    application ? serviceCardStatus({ status: application.status }) : "unknown",
  );
  const ownership = $derived(
    application ? applicationOwnership(application) : "unknown",
  );
  const lockedComponent = $derived(
    application?.components.find((component) =>
      serviceIsLocked(component.mutation_lock),
    ),
  );
  const logComponent = $derived(
    application
      ? (application.components.find(
          (component) => component.id === logComponentId,
        ) ??
          logsTarget(application) ??
          application.components[0])
      : undefined,
  );
  const openUrl = $derived(
    application ? serviceOpenUrl(application.access) : undefined,
  );

  async function requestApplicationAction(action: ApplicationAction) {
    if (!application) return;
    actionError = null;
    const error = await runApplicationAction(tracker, application, action);
    if (error) actionError = error;
  }

  /** Collect one redacted log page for a component through the governed job. */
  async function fetchLogs(component: CanonicalService) {
    actionError = null;
    const error = await tracker.run(component, "logs");
    if (error) actionError = error;
  }

  async function requestComponentAction(
    component: CanonicalService,
    action: GovernedServiceAction,
  ) {
    if (action === "logs") {
      selectLogComponent(component.id);
      setTab("logs");
      return;
    }
    actionError = null;
    const error = await runComponentAction(tracker, component, action);
    if (error) actionError = error;
  }

  const heroActions = $derived(
    application
      ? applicationCardActions(application, tracker, {
          onLogs: (target) => {
            selectLogComponent(target.id);
            setTab("logs");
          },
          onRun: (action) => void requestApplicationAction(action),
          onAdopt: (target) => (adoption = { service: target, release: false }),
          onOpen: (url) => openServiceUrl({ url }),
        })
      : [],
  );

  function componentActions(component: CanonicalService) {
    const url = serviceOpenUrl({
      open_url:
        typeof component.access.url === "string"
          ? component.access.url
          : undefined,
    });
    return componentCardActions(component, tracker, {
      onAction: (action) => void requestComponentAction(component, action),
      onAdopt: () => (adoption = { service: component, release: false }),
      onOpen: url ? () => openServiceUrl({ url }) : undefined,
    });
  }

  function componentPlacement(component: CanonicalService) {
    return runtimePlacement(
      component,
      registryServiceFor(component),
      placementContext,
    );
  }

  // Logs: fetch once when the tab opens on a component that can serve them.
  $effect(() => {
    const component = logComponent;
    if (tab !== "logs" || !component) return;
    untrack(() => {
      if (
        component.allowed_actions.includes("logs") &&
        !tracker.logs[component.id] &&
        !isActionActive(tracker.jobs[component.id])
      ) {
        void fetchLogs(component);
      }
    });
  });

  // Activity: load the recorded operations once when the tab first opens.
  $effect(() => {
    if (tab !== "activity" || !application) return;
    untrack(() => {
      if (!recorded.loaded && !recorded.loading) void loadActivity();
    });
  });

  function receiptOf(job: JobDetail): Record<string, unknown> | undefined {
    const result = job.result;
    if (!result || typeof result !== "object") return undefined;
    const receipt = (result as Record<string, unknown>).service_action_receipt;
    return receipt && typeof receipt === "object"
      ? (receipt as Record<string, unknown>)
      : undefined;
  }

  async function loadActivity() {
    if (!application) return;
    const ids = new Set(
      application.components.map((component) => component.id),
    );
    recorded = { ...recorded, loading: true, error: null };
    try {
      const result = await listJobs({
        kitDeploymentId: application.kit_deployment_id,
        limit: 100,
      });
      const jobs = result.items.filter((job) => {
        const receipt = receiptOf(job);
        const payloadService = job.payload?.service_id;
        return (
          (typeof receipt?.service_id === "string" &&
            ids.has(receipt.service_id)) ||
          (typeof payloadService === "string" && ids.has(payloadService)) ||
          (job.target_id !== undefined && ids.has(job.target_id))
        );
      });
      recorded = { loading: false, loaded: true, error: null, jobs };
    } catch (cause) {
      recorded = {
        ...recorded,
        loading: false,
        loaded: true,
        error:
          cause instanceof Error
            ? cause.message
            : tr("ui.serviceDetail.activityLoadFailed"),
      };
    }
  }

  interface ActivityRow {
    id: string;
    title: string;
    state: string;
    time: string;
    detail?: string;
    session: boolean;
  }

  const activityRows = $derived.by<ActivityRow[]>(() => {
    if (!application) return [];
    const names = new Map(
      application.components.map((component) => [component.id, component.name]),
    );
    const recordedIds = new Set(recorded.jobs.map((job) => job.id));
    const rows: ActivityRow[] = recorded.jobs.map((job) => {
      const receipt = receiptOf(job);
      const serviceId =
        typeof receipt?.service_id === "string" ? receipt.service_id : "";
      const action = typeof receipt?.action === "string" ? receipt.action : "";
      return {
        id: job.id,
        title: [
          action ? actionLabel(action) : stateLabel(job.type),
          names.get(serviceId),
        ]
          .filter(Boolean)
          .join(" · "),
        state: job.state,
        time: job.updated_at || job.created_at,
        detail: job.error || job.message || undefined,
        session: false,
      };
    });
    for (const entry of tracker.history) {
      if (entry.jobId && recordedIds.has(entry.jobId)) continue;
      rows.push({
        id: entry.id,
        title: `${actionLabel(entry.action)} · ${entry.serviceName}`,
        state: entry.state,
        time: entry.updatedAt,
        detail: entry.detail,
        session: true,
      });
    }
    return rows.sort((left, right) => right.time.localeCompare(left.time));
  });

  function stateTone(state: string): string {
    const value = state.toLowerCase();
    if (["completed", "running", "healthy", "reachable"].includes(value)) {
      return "border-success/40 text-success";
    }
    if (
      ["failed", "error", "cancelled", "canceled", "unhealthy"].includes(value)
    ) {
      return "border-destructive/40 text-destructive";
    }
    return "border-border text-muted-foreground";
  }

  function when(value?: string): string {
    return value ? formatDateTime(value) : tr("ui.serviceCards.notObserved");
  }

  const logState = $derived(
    logComponent ? tracker.logs[logComponent.id] : undefined,
  );
  const logJob = $derived(
    logComponent ? tracker.jobs[logComponent.id] : undefined,
  );

  function onAdoptionClose() {
    adoption = null;
  }

  const accessUrls = $derived(
    (application?.components ?? []).flatMap((component) => {
      const url = serviceOpenUrl({
        open_url:
          typeof component.access.url === "string"
            ? component.access.url
            : undefined,
      });
      return url ? [{ component, url }] : [];
    }),
  );
</script>

{#if adoption}{#key `${adoption.service.id}:${adoption.release}`}<ServiceAdoptionDialog
      service={adoption.service}
      release={adoption.release}
      onClose={onAdoptionClose}
      onComplete={reloadQuietly}
    />{/key}{/if}

<div class="p-4 md:p-6" data-testid="service-application-detail">
  <PageHeader
    title={application?.display_name ||
      tr("ui.servicesApplicationId.applicationDetails")}
  >
    {#snippet actions()}
      <Button variant="secondary" onclick={() => goto("/services")}>
        <ArrowLeft class="h-4 w-4" />
        {tr("ui.servicesApplicationId.backToApplications")}
      </Button>
    {/snippet}
  </PageHeader>

  {#if loading}
    <LoadingNote
      label={tr("ui.serviceDetail.loading")}
      testid="application-detail-loading"
    />
  {:else if loadError}
    <div
      class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-destructive/30 bg-destructive/10 p-4 text-destructive"
      role="alert"
      data-testid="application-detail-error"
    >
      <span class="min-w-0 break-words">{loadError}</span>
      <Button variant="secondary" size="sm" onclick={() => void load()}>
        <RefreshCw class="h-3.5 w-3.5" />
        {tr("ui.serviceDetail.retry")}
      </Button>
    </div>
  {:else if application}
    {#if actionError}
      <div
        class="mb-4 rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive"
        role="alert"
        data-testid="application-detail-action-error"
      >
        {actionError}
      </div>
    {/if}

    <div
      role="tablist"
      aria-label={tr("ui.serviceDetail.tabsLabel")}
      class="mb-6 flex w-full flex-wrap gap-1 rounded-lg border border-border bg-card/60 p-1 text-sm shadow-sm sm:w-fit"
    >
      {#each TABS as id, index (id)}
        <button
          type="button"
          role="tab"
          id="application-tab-{id}"
          aria-selected={tab === id}
          aria-controls="application-panel-{id}"
          tabindex={tab === id ? 0 : -1}
          data-testid="application-tab-{id}"
          class="cursor-pointer rounded-md px-4 py-2 font-medium transition-all {tab ===
          id
            ? 'bg-muted font-semibold text-foreground shadow-sm'
            : 'text-muted-foreground hover:text-foreground'}"
          onclick={() => setTab(id)}
          onkeydown={(event) => onTabKeydown(event, index)}
        >
          {tr(`ui.serviceDetail.tab.${id}`)}
          {#if id === "components"}
            <span class="ml-1 text-xs text-muted-foreground"
              >{application.components.length}</span
            >
          {/if}
        </button>
      {/each}
    </div>

    {#if tab === "overview"}
      <div
        role="tabpanel"
        id="application-panel-overview"
        aria-labelledby="application-tab-overview"
        class="grid gap-6 xl:grid-cols-[minmax(0,1fr)_22rem]"
        data-testid="application-panel-overview"
      >
        <div class="min-w-0 space-y-6">
          <BrandLogoScope domain={logoDomain}>
            <ServiceCard
              name={application.display_name}
              description={trn(
                "ui.services.componentsOn",
                application.components.length,
                {
                  server: server?.name || application.server_id,
                },
              )}
              address={application.access.address}
              addressUnavailableReason={application.access.address
                ? undefined
                : stateLabel(
                    application.access.reason ||
                      tr("ui.servicesApplicationId.noReachableAddressReported"),
                  )}
              icon={BrandLogoIcon}
              logoSrc={serviceCardLogoSrc(logoDomain)}
              version={serviceCardVersion(version)}
              update={serviceCardUpdate(version)}
              {placement}
              {status}
              statusLabel={stateLabel(application.status)}
              statusMessage={status === "error" ||
              heroActions.some((a) => a.id === "adopt")
                ? undefined
                : serviceCardStatusMessage({ status: application.status })}
              managementLabel={ownership === "managed"
                ? tr("ui.services.stacked")
                : ownership === "observed"
                  ? tr("ui.services.unstacked")
                  : ownership === "mixed"
                    ? tr("ui.serviceCards.ownershipMixed")
                    : undefined}
              locked={applicationLocked(application)}
              lockedReason={serviceLockReason(lockedComponent?.mutation_lock)}
              actions={heroActions}
            />
          </BrandLogoScope>

          {#each application.components as component (component.id)}
            {@const job = tracker.jobs[component.id]}
            {#if job && (job.action !== "logs" || actionFailed(job))}
              <div
                class="rounded-lg border border-border/70 bg-muted/25 p-3 text-xs"
                role={actionFailed(job) ? "alert" : "status"}
                data-testid="application-job-status"
              >
                <p class="font-medium text-foreground">
                  {tr("ui.serviceCards.jobLine", {
                    name: component.name,
                    action: actionLabel(job.action),
                    state: stateLabel(job.state),
                  })}
                  {#if job.converging}{tr("ui.services.verifying")}{/if}
                </p>
                {#if job.detail}<p class="mt-1 text-muted-foreground">
                    {job.detail}
                  </p>{/if}
              </div>
            {/if}
          {/each}

          <section
            class="rounded-lg border border-border bg-card/60 p-4"
            aria-labelledby="version-title"
          >
            <h2 id="version-title" class="text-base font-semibold">
              {tr("ui.serviceDetail.version.title")}
            </h2>
            {#if version}
              <dl class="mt-3 grid gap-3 text-sm sm:grid-cols-2">
                <div>
                  <dt class="text-muted-foreground">
                    {tr("ui.serviceDetail.version.installed")}
                  </dt>
                  <dd class="font-mono">
                    {version.current ??
                      tr("ui.serviceDetail.version.notMeasured")}
                  </dd>
                </div>
                <div>
                  <dt class="text-muted-foreground">
                    {tr("ui.serviceDetail.version.latest")}
                  </dt>
                  <dd class="font-mono">
                    {#if version.latest}
                      {version.latest}
                      {#if !version.update_available}
                        <span class="ml-1 font-sans text-xs text-success"
                          >{tr("ui.serviceDetail.version.upToDate")}</span
                        >
                      {/if}
                    {:else}
                      <span class="font-sans text-muted-foreground">
                        {version.reason_code
                          ? tr(
                              "ui.serviceDetail.version.latestUnknownBecause",
                              {
                                reason: stateLabel(version.reason_code),
                              },
                            )
                          : tr("ui.serviceDetail.version.notMeasured")}
                      </span>
                    {/if}
                  </dd>
                </div>
                {#if version.image}
                  <div class="sm:col-span-2">
                    <dt class="text-muted-foreground">
                      {tr("ui.serviceDetail.version.image")}
                    </dt>
                    <dd class="break-all font-mono text-xs">{version.image}</dd>
                  </div>
                {/if}
                {#if version.source}
                  <div>
                    <dt class="text-muted-foreground">
                      {tr("ui.servicesApplicationId.source")}
                    </dt>
                    <dd>{stateLabel(version.source)}</dd>
                  </div>
                {/if}
                {#if version.update_mode}
                  <div>
                    <dt class="text-muted-foreground">
                      {tr("ui.serviceDetail.version.updatePath")}
                    </dt>
                    <dd>{stateLabel(version.update_mode)}</dd>
                  </div>
                {/if}
                <div>
                  <dt class="text-muted-foreground">
                    {tr("ui.serviceDetail.version.checked")}
                  </dt>
                  <dd>{when(version.checked_at)}</dd>
                </div>
              </dl>
            {:else}
              <p class="mt-2 text-sm text-muted-foreground">
                {tr("ui.serviceDetail.version.none")}
              </p>
            {/if}
          </section>

          <section
            class="rounded-lg border border-border bg-card/60 p-4"
            aria-labelledby="access-title"
          >
            <h2 id="access-title" class="text-base font-semibold">
              {tr("ui.serviceDetail.access.title")}
            </h2>
            <div class="mt-3 flex flex-wrap items-center gap-3">
              {#if application.access.address || openUrl}
                <span class="min-w-0 flex-1 truncate font-mono text-sm"
                  >{openUrl ?? application.access.address}</span
                >
                {#if openUrl}
                  <Button
                    size="sm"
                    onclick={() => openServiceUrl({ url: openUrl })}
                  >
                    <ExternalLink class="h-3.5 w-3.5" />
                    {tr("ui.servicesApplicationId.openService")}
                  </Button>
                {/if}
              {:else}
                <p class="text-sm text-muted-foreground">
                  {stateLabel(
                    application.access.reason ||
                      tr("ui.servicesApplicationId.noReachableAddressReported"),
                  )}
                </p>
              {/if}
            </div>
            <p class="mt-2 text-xs text-muted-foreground">
              {tr("ui.servicesApplicationId.addressClass")}: {stateLabel(
                application.access.kind,
              )}
              {#if application.access.kind === "internal"}
                · {tr("ui.services.internalAddressOnlyOpenIt")}
              {/if}
            </p>
            {#if accessUrls.length > 0}
              <ul class="mt-3 space-y-1.5">
                {#each accessUrls as entry (entry.component.id)}
                  <li
                    class="flex items-center gap-2 rounded-md bg-muted/30 px-2.5 py-1.5 text-xs"
                  >
                    <span class="font-medium">{entry.component.name}</span>
                    <span
                      class="min-w-0 flex-1 truncate font-mono text-muted-foreground"
                      >{entry.url}</span
                    >
                    <Button
                      variant="ghost"
                      size="sm"
                      onclick={() => openServiceUrl({ url: entry.url })}
                      >{tr("ui.services.open")}</Button
                    >
                  </li>
                {/each}
              </ul>
            {/if}
          </section>

          <section
            class="rounded-lg border border-border bg-card/60 p-4"
            aria-labelledby="health-title"
          >
            <h2 id="health-title" class="text-base font-semibold">
              {tr("ui.services.health")}
            </h2>
            {#if application.components.length === 0}
              <p class="mt-2 text-sm text-muted-foreground">
                {tr("ui.serviceDetail.components.empty")}
              </p>
            {:else}
              <ul class="mt-3 grid gap-2 sm:grid-cols-2">
                {#each application.components as component (component.id)}
                  <li
                    class="flex items-center gap-2.5 rounded-lg bg-muted/30 px-2.5 py-2"
                  >
                    <span
                      class="h-2 w-2 shrink-0 rounded-full {[
                        'healthy',
                        'running',
                        'reachable',
                      ].includes(componentState(component).toLowerCase())
                        ? 'bg-success'
                        : 'bg-warning'}"
                    ></span>
                    <span class="min-w-0 flex-1 truncate font-mono text-xs"
                      >{component.name || component.service_key}</span
                    >
                    <span class="shrink-0 text-xs text-muted-foreground"
                      >{stateLabel(componentState(component))}</span
                    >
                  </li>
                {/each}
              </ul>
            {/if}
          </section>
        </div>

        <aside
          class="h-fit rounded-lg border border-border bg-card/60 p-4"
          aria-labelledby="facts-title"
        >
          <div class="flex items-center gap-2">
            <Server class="h-4 w-4 text-primary" />
            <h2 id="facts-title" class="text-base font-semibold">
              {tr("ui.servicesApplicationId.runtimeAssignment")}
            </h2>
          </div>
          <dl class="mt-4 space-y-3 text-sm">
            <div>
              <dt class="text-muted-foreground">
                {tr("ui.servicesApplicationId.server")}
              </dt>
              <dd class="break-all">
                {server?.name || application.server_id}
                {#if server?.hostname && server.hostname !== server.name}
                  <span class="block font-mono text-xs text-muted-foreground"
                    >{server.hostname}</span
                  >
                {/if}
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground">
                {tr("ui.serviceDetail.facts.stack")}
              </dt>
              <dd class="break-all">
                <a
                  class="text-primary hover:underline"
                  href="/stacks/{encodeURIComponent(
                    application.kit_deployment_id,
                  )}">{stack?.name || application.kit_deployment_id}</a
                >
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground">
                {tr("ui.serviceDetail.facts.placement")}
              </dt>
              <dd>{tr(`ui.services.placement.${placement}`)}</dd>
            </div>
            <div>
              <dt class="text-muted-foreground">
                {tr("ui.servicesApplicationId.management")}
              </dt>
              <dd>
                {ownership === "managed"
                  ? tr("ui.services.stacked")
                  : ownership === "observed"
                    ? tr("ui.services.unstacked")
                    : ownership === "mixed"
                      ? tr("ui.serviceCards.ownershipMixed")
                      : tr("ui.common.unknown")}
              </dd>
            </div>
            {#if application.catalog?.category}
              <div>
                <dt class="text-muted-foreground">
                  {tr("ui.serviceDetail.facts.category")}
                </dt>
                <dd>{stateLabel(application.catalog.category)}</dd>
              </div>
            {/if}
            <div>
              <dt class="text-muted-foreground">
                {tr("ui.servicesApplicationId.applicationKey")}
              </dt>
              <dd class="font-mono text-xs">{application.application_key}</dd>
            </div>
            <div>
              <dt class="text-muted-foreground">
                {tr("ui.servicesApplicationId.freshness")}
              </dt>
              <dd>{when(application.observed_at)}</dd>
            </div>
          </dl>
        </aside>
      </div>
    {:else if tab === "components"}
      <div
        role="tabpanel"
        id="application-panel-components"
        aria-labelledby="application-tab-components"
        data-testid="application-panel-components"
      >
        {#if application.components.length === 0}
          <p
            class="rounded-lg border border-dashed border-border bg-background/40 p-6 text-center text-sm text-muted-foreground"
          >
            {tr("ui.serviceDetail.components.empty")}
          </p>
        {:else}
          <div class="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
            {#each application.components as component (component.id)}
              {@const job = tracker.jobs[component.id]}
              {@const componentStatus = serviceCardStatus({
                status: componentState(component),
                management_state: component.management_state,
              })}
              <BrandLogoScope
                domain={serviceCardLogoDomain(
                  component.catalog,
                  component.service_key,
                  component.name,
                )}
              >
                <div
                  data-testid="application-component"
                  data-service-id={component.id}
                >
                  <ServiceCard
                    name={component.name || component.service_key}
                    description={[component.service_key, component.role]
                      .filter(Boolean)
                      .join(" · ")}
                    icon={BrandLogoIcon}
                    logoSrc={serviceCardLogoSrc(
                      serviceCardLogoDomain(
                        component.catalog,
                        component.service_key,
                        component.name,
                      ),
                    )}
                    version={serviceCardVersion(component.version)}
                    update={serviceCardUpdate(component.version)}
                    placement={componentPlacement(component)}
                    status={componentStatus}
                    statusLabel={stateLabel(componentState(component))}
                    statusMessage={serviceCardStatusMessage({
                      status: componentState(component),
                      management_state: component.management_state,
                    })}
                    managementLabel={component.management_state === "managed"
                      ? tr("ui.services.stacked")
                      : tr("ui.services.unstacked")}
                    desiredState={component.desired_state !==
                    component.observed_state
                      ? component.desired_state || undefined
                      : undefined}
                    observedState={component.desired_state !==
                    component.observed_state
                      ? component.observed_state || undefined
                      : undefined}
                    locked={serviceIsLocked(component.mutation_lock)}
                    lockedReason={serviceLockReason(component.mutation_lock)}
                    actions={componentActions(component)}
                  />
                  {#if job && (job.action !== "logs" || actionFailed(job))}
                    <div
                      class="mt-2 rounded border border-border/70 bg-muted/25 p-2 text-xs"
                      role={actionFailed(job) ? "alert" : "status"}
                    >
                      <p class="font-medium text-foreground">
                        {actionLabel(job.action)}: {stateLabel(job.state)}
                        {#if job.converging}{tr("ui.services.verifying")}{/if}
                      </p>
                      {#if job.detail}<p class="mt-1 text-muted-foreground">
                          {job.detail}
                        </p>{/if}
                    </div>
                  {/if}
                </div>
              </BrandLogoScope>
            {/each}
          </div>
        {/if}
      </div>
    {:else if tab === "logs"}
      <div
        role="tabpanel"
        id="application-panel-logs"
        aria-labelledby="application-tab-logs"
        class="space-y-4"
        data-testid="application-panel-logs"
      >
        {#if !logComponent}
          <p
            class="rounded-lg border border-dashed border-border bg-background/40 p-6 text-center text-sm text-muted-foreground"
          >
            {tr("ui.serviceDetail.components.empty")}
          </p>
        {:else}
          <div class="flex flex-wrap items-center gap-2">
            <div
              class="flex flex-wrap gap-1"
              role="group"
              aria-label={tr("ui.serviceDetail.logs.component")}
            >
              {#each application.components as component (component.id)}
                <button
                  type="button"
                  aria-pressed={logComponent.id === component.id}
                  class="cursor-pointer rounded-md border px-3 py-1.5 text-xs font-medium transition-colors {logComponent.id ===
                  component.id
                    ? 'border-primary bg-primary/10 text-foreground'
                    : 'border-border text-muted-foreground hover:text-foreground'}"
                  onclick={() => selectLogComponent(component.id)}
                  >{component.name || component.service_key}</button
                >
              {/each}
            </div>
            <div class="ml-auto flex items-center gap-2">
              {#if logState?.loading || (logJob?.action === "logs" && isActionActive(logJob))}
                <Loader2
                  class="h-3.5 w-3.5 animate-spin text-muted-foreground"
                />
              {/if}
              <Button
                variant="secondary"
                size="sm"
                disabled={!logComponent.allowed_actions.includes("logs") ||
                  isActionActive(logJob)}
                onclick={() => void fetchLogs(logComponent)}
              >
                <RefreshCw class="h-3.5 w-3.5" />
                {logState
                  ? tr("ui.services.refreshLogs")
                  : tr("ui.services.fetchLogs")}
              </Button>
            </div>
          </div>

          {#if !logComponent.allowed_actions.includes("logs")}
            <p class="text-sm text-muted-foreground">
              {tr("ui.serviceDetail.logs.notGranted")}
            </p>
          {/if}
          {#if logState?.error}
            <p class="text-sm text-destructive" role="alert">
              {logState.error}
            </p>
          {/if}
          {#if logState && logState.entries.length > 0}
            <ol
              class="max-h-[32rem] space-y-1 overflow-auto rounded-lg border border-border bg-background/60 p-2 font-mono text-[11px]"
              data-testid="application-log-entries"
            >
              {#each logState.entries as entry (`${entry.timestamp}-${entry.message}`)}
                <li class="rounded p-1.5 odd:bg-muted/20">
                  <time class="text-muted-foreground" datetime={entry.timestamp}
                    >{when(entry.timestamp)}</time
                  >
                  <pre
                    class="whitespace-pre-wrap break-words text-foreground">{entry.message}</pre>
                </li>
              {/each}
            </ol>
            {#if logState.nextCursor}
              <Button
                variant="secondary"
                size="sm"
                disabled={isActionActive(logJob)}
                onclick={() => tracker.loadMoreLogs(logComponent)}
                >{tr("ui.services.loadOlderLogs")}</Button
              >
            {/if}
          {:else if logState && !logState.loading && !logState.error}
            <p class="text-sm text-muted-foreground">
              {tr("ui.services.theLastLogPageWas")}
            </p>
          {/if}
        {/if}
      </div>
    {:else if tab === "activity"}
      <div
        role="tabpanel"
        id="application-panel-activity"
        aria-labelledby="application-tab-activity"
        class="space-y-4"
        data-testid="application-panel-activity"
      >
        <div class="flex items-center justify-between gap-3">
          <p class="text-xs text-muted-foreground">
            {tr("ui.serviceDetail.activity.hint")}
          </p>
          <Button
            variant="secondary"
            size="sm"
            disabled={recorded.loading}
            onclick={() => void loadActivity()}
          >
            <RefreshCw class="h-3.5 w-3.5" />
            {tr("ui.serviceDetail.activity.refresh")}
          </Button>
        </div>
        {#if recorded.error}
          <p class="text-sm text-destructive" role="alert">
            {recorded.error}
          </p>
        {/if}
        {#if recorded.loading && activityRows.length === 0}
          <LoadingNote label={tr("common.loading")} />
        {:else if activityRows.length === 0}
          <p
            class="rounded-lg border border-dashed border-border bg-background/40 p-6 text-center text-sm text-muted-foreground"
          >
            {tr("ui.services.noGovernedActionHasRun")}
          </p>
        {:else}
          <ol class="space-y-2" data-testid="application-activity-list">
            {#each activityRows as row (row.id)}
              <li
                class="flex flex-wrap items-start gap-3 rounded-lg border border-border bg-card/60 p-3"
              >
                <Activity class="mt-0.5 h-4 w-4 shrink-0 text-primary" />
                <div class="min-w-0 flex-1">
                  <p class="text-sm font-medium">{row.title}</p>
                  <p class="text-xs text-muted-foreground">
                    {when(row.time)}
                    {#if row.session}
                      · {tr("ui.serviceDetail.activity.thisSession")}
                    {/if}
                  </p>
                  {#if row.detail}
                    <p class="mt-1 break-words text-xs text-muted-foreground">
                      {row.detail}
                    </p>
                  {/if}
                </div>
                <span
                  class="rounded-full border px-2 py-0.5 text-xs {stateTone(
                    row.state,
                  )}">{stateLabel(row.state)}</span
                >
              </li>
            {/each}
          </ol>
        {/if}
      </div>
    {:else}
      <div
        role="tabpanel"
        id="application-panel-settings"
        aria-labelledby="application-tab-settings"
        class="space-y-4"
        data-testid="application-panel-settings"
      >
        <p class="text-xs text-muted-foreground">
          {tr("ui.services.mutationsRequireOwnerApprovalA")}
        </p>
        {#if application.components.length === 0}
          <p
            class="rounded-lg border border-dashed border-border bg-background/40 p-6 text-center text-sm text-muted-foreground"
          >
            {tr("ui.serviceDetail.components.empty")}
          </p>
        {/if}
        {#each application.components as component (component.id)}
          {@const locked = serviceIsLocked(component.mutation_lock)}
          {@const job = tracker.jobs[component.id]}
          <article
            class="rounded-lg border border-border bg-card/60 p-4"
            data-testid="application-settings-component"
            data-service-id={component.id}
          >
            <div class="flex flex-wrap items-start justify-between gap-3">
              <div class="min-w-0">
                <h3 class="text-sm font-semibold">
                  {component.name || component.service_key}
                </h3>
                <p class="mt-1 font-mono text-xs text-muted-foreground">
                  {component.service_key}
                </p>
              </div>
              <span
                class="rounded-full border px-2 py-0.5 text-xs {locked
                  ? 'border-warning/40 text-warning'
                  : 'border-border text-muted-foreground'}"
                >{locked
                  ? (serviceLockReason(component.mutation_lock) ??
                    tr("ui.serviceCardAdapter.locked"))
                  : tr("ui.serviceDetail.settings.unlocked")}</span
              >
            </div>

            <div class="mt-4 flex flex-wrap gap-2">
              {#if locked && component.allowed_actions.includes("unfreeze")}
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={isActionActive(job)}
                  onclick={() =>
                    void requestComponentAction(component, "unfreeze")}
                  >{tr("ui.serviceDetail.settings.unlock")}</Button
                >
              {:else if !locked && component.allowed_actions.includes("freeze")}
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={isActionActive(job)}
                  onclick={() =>
                    void requestComponentAction(component, "freeze")}
                  >{tr("ui.serviceDetail.settings.lock")}</Button
                >
              {:else}
                <p class="text-xs text-muted-foreground">
                  {tr("ui.serviceDetail.settings.lockUnavailable")}
                </p>
              {/if}

              {#if component.management_state === "observed"}
                <Button
                  variant="primary"
                  size="sm"
                  testId="stack-it-component"
                  disabled={!component.adoption?.preview_available}
                  onclick={() =>
                    (adoption = { service: component, release: false })}
                  >{tr("ui.services.stackIt")}</Button
                >
                {#if !component.adoption?.preview_available}
                  <p class="w-full text-xs text-muted-foreground">
                    {tr("ui.services.adoptionUnavailable")}
                  </p>
                {/if}
              {/if}
              {#if component.adoption?.release_available}
                <Button
                  variant="secondary"
                  size="sm"
                  onclick={() =>
                    (adoption = { service: component, release: true })}
                  >{tr("ui.services.releaseBinding")}</Button
                >
              {/if}
            </div>
            <dl class="mt-4 grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
              <div>
                <dt class="text-muted-foreground">
                  {tr("ui.servicesApplicationId.role")}
                </dt>
                <dd>
                  {component.role
                    ? stateLabel(component.role)
                    : tr("ui.serviceDetail.settings.unclassified")}
                </dd>
              </div>
              <div>
                <dt class="text-muted-foreground">
                  {tr("ui.servicesApplicationId.lifecycle")}
                </dt>
                <dd>
                  {component.lifecycle
                    ? stateLabel(component.lifecycle)
                    : tr("ui.common.unknown")}
                </dd>
              </div>
              <div>
                <dt class="text-muted-foreground">
                  {tr("ui.servicesApplicationId.impact")}
                </dt>
                <dd>
                  {component.operational_impact
                    ? stateLabel(component.operational_impact)
                    : tr("ui.common.unknown")}
                </dd>
              </div>
              <div>
                <dt class="text-muted-foreground">
                  {tr("ui.servicesApplicationId.source")}
                </dt>
                <dd>{stateLabel(component.source)}</dd>
              </div>
            </dl>
          </article>
        {/each}
      </div>
    {/if}
  {/if}
</div>
