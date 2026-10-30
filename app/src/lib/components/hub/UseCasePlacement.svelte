<!--
  Where new use cases run: one of the owner's existing StackKits, an enrolled
  server without a StackKit, or a new server. Existing targets come first.

  With selected use cases every StackKit is checked for fit (StackKits
  feasibility) and the best verified one is preselected. Without a selection
  the owner picks the target first and the use cases on its Goals step.

  The chosen target is part of the address (`?target=`), so the browser's back
  button returns from the wizard to this choice and a reload keeps it.
-->
<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { pushState, replaceState } from "$app/navigation";
  import { page } from "$app/state";
  import {
    resolveSetup,
    setupChanges,
    setupFlowFromCatalog,
  } from "@kombiverselabs/ui/usecase";
  import {
    loadUseCaseCatalog,
    type UseCaseCatalogView,
  } from "#lib/api/useCaseCatalog.js";
  import { supportsCreationChoice } from "#lib/wizard/creationGoals.js";
  import {
    listCanonicalServers,
    isCurrentCanonicalServer,
    type CanonicalServer,
  } from "#lib/api/registry.js";
  import { listKitDeployments, type KitDeployment } from "#lib/api/stacks.js";
  import {
    acceptableLayoutRecommendation,
    previewLayoutRecommendation,
    type LayoutRecommendationView,
  } from "#lib/api/layout.js";
  import {
    buildLayoutSelection,
    selectedCanonicalUseCasesFromGoals,
  } from "#lib/wizard/index.js";
  import type { StackConfig } from "#lib/wizard/types.js";
  import type { OperatorSelfDisclosure } from "#lib/api/operatorProfile.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import { tr } from "#lib/i18n.svelte.js";
  import Button from "#lib/components/ui/Button.svelte";
  import LoadingNote from "#lib/components/ui/LoadingNote.svelte";
  import ErrorCallout from "#lib/components/ui/ErrorCallout.svelte";
  import LayoutRecommendationPanel from "#lib/components/wizard/LayoutRecommendationPanel.svelte";
  import ConfigurationFlow from "./ConfigurationFlow.svelte";
  import AddUseCasesFlow from "./AddUseCasesFlow.svelte";

  let {
    initialConfig,
    selfDisclosure = null,
    onSelfDisclosureSettled,
    preferredServerId = "",
    newWhenNoTargets = false,
  }: {
    /** The use cases to place; without them the target's Goals step asks. */
    initialConfig?: StackConfig;
    selfDisclosure?: OperatorSelfDisclosure | null;
    onSelfDisclosureSettled?: () => void;
    /** The server the owner started from: it, or the StackKit it runs. */
    preferredServerId?: string;
    /** Open the new-server wizard at once when there is nothing to choose. */
    newWhenNoTargets?: boolean;
  } = $props();

  type Candidate = {
    deployment: KitDeployment;
    view?: LayoutRecommendationView;
    unavailable?: boolean;
  };
  let resolvedConfig = $state<StackConfig | undefined>(
    untrack(() => initialConfig),
  );
  let deployments = $state<KitDeployment[]>([]);
  let bareServers = $state<CanonicalServer[]>([]);
  let preferredServer = $state<CanonicalServer | undefined>();
  let serverNames = $state(new Map<string, string[]>());
  const installable = (server: CanonicalServer) =>
    server.connection.state === "connected" &&
    server.mutations_allowed &&
    !server.provider.lease_id;
  let candidates = $state<Candidate[]>([]);
  let fetched = $state(false);
  let checking = $state(false);
  let targetsChecked = $state(false);
  let failed = $state(false);
  let selected = $state("");
  let requested = $state(false);
  let mounted = $state(false);
  let controller: AbortController | undefined;

  const currentUrl = $derived(page.shallow?.url ?? page.url);
  const destination = $derived(currentUrl.searchParams.get("target") || null);
  const hasGoals = $derived(
    selectedCanonicalUseCasesFromGoals(resolvedConfig?.goals).length > 0,
  );
  // Guided answers become settings only against the catalog; the wizard
  // waits for that so the chosen target sees the same selection.
  const needsResolution = $derived(
    Object.keys(initialConfig?.useCaseSetupAnswers ?? {}).length > 0,
  );
  const chosen = $derived(
    candidates.find((entry) => entry.deployment.id === selected),
  );
  // Without selected use cases there is nothing to verify yet: the chosen
  // StackKit's Goals step shows the verified distribution of the selection.
  const compatible = (candidate: Candidate) =>
    candidate.view
      ? candidate.view.outcome === "feasible" &&
        Boolean(acceptableLayoutRecommendation(candidate.view))
      : !hasGoals && !candidate.unavailable;
  // The server the owner started from was not preselected: say why, instead of
  // silently offering another target.
  let preferredSkipped = $state<{ key: string; name?: string } | null>(null);
  const canContinue = $derived(
    selected === "new" ||
      (selected.startsWith("server:") &&
        bareServers.some(
          (server) => `server:${server.id}` === selected && installable(server),
        )) ||
      Boolean(chosen && compatible(chosen)),
  );

  onMount(() => {
    mounted = true;
    return () => controller?.abort();
  });
  $effect(() => {
    if (!mounted || authStore.loading || !authStore.modeDetected || requested)
      return;
    if (!authStore.isAuthenticated) {
      // Signed out, or an embed still waiting for its session: only a new
      // server is offered until the session arrives and the targets load.
      fetched = true;
      targetsChecked = true;
      candidates = [];
      selected = "new";
      return;
    }
    requested = true;
    void load();
  });
  // The fit of each StackKit is checked only while the choice is on screen.
  $effect(() => {
    if (!fetched || failed || destination || targetsChecked || checking) return;
    untrack(() => void checkTargets());
  });

  function choose(target: string, replace = false) {
    if (currentUrl.searchParams.get("target") === target) return;
    const url = new URL(currentUrl.href);
    url.searchParams.set("target", target);
    url.searchParams.delete("step");
    const write = replace ? replaceState : pushState;
    write(`${url.pathname}${url.search}${url.hash}`, page.state);
  }

  function changeTarget() {
    const url = new URL(currentUrl.href);
    url.searchParams.delete("target");
    url.searchParams.delete("step");
    pushState(`${url.pathname}${url.search}${url.hash}`, page.state);
  }

  function resolveSetupAnswers(
    config: StackConfig,
    catalog: Map<string, UseCaseCatalogView>,
  ): StackConfig {
    const resolved = {
      ...config,
      useCaseSettings: { ...config.useCaseSettings },
      useCaseCapabilities: { ...config.useCaseCapabilities },
    };
    for (const [goal, answers] of Object.entries(
      config.useCaseSetupAnswers ?? {},
    )) {
      const view = catalog.get(goal);
      const flow = setupFlowFromCatalog(goal, view);
      if (!flow) continue;
      const changes = setupChanges(
        view,
        resolveSetup(flow, answers),
        resolved.useCaseSettings[goal],
      );
      resolved.useCaseSettings[goal] = { ...resolved.useCaseSettings[goal] };
      for (const [id, value] of changes.settings) {
        if (supportsCreationChoice(view, id, value))
          resolved.useCaseSettings[goal][id] = value;
      }
      if (view?.capabilities?.length)
        resolved.useCaseCapabilities[goal] = changes.capabilities;
    }
    return resolved;
  }

  async function load() {
    controller?.abort();
    const attempt = new AbortController();
    controller = attempt;
    fetched = false;
    failed = false;
    targetsChecked = false;
    try {
      // The catalog is read only to turn handed-over setup answers into
      // settings; without answers it never delays the choice.
      const [listedDeployments, servers, catalog] = await Promise.all([
        listKitDeployments(),
        listCanonicalServers(),
        needsResolution ? loadUseCaseCatalog() : undefined,
      ]);
      if (attempt.signal.aborted) return;
      if (initialConfig && catalog)
        resolvedConfig = resolveSetupAnswers(initialConfig, catalog);
      const current = servers.filter(
        (server) =>
          isCurrentCanonicalServer(server) && server.node_role !== "substrate",
      );
      const names = new Map<string, string[]>();
      for (const server of current) {
        if (!server.kit_deployment_id) continue;
        names.set(server.kit_deployment_id, [
          ...(names.get(server.kit_deployment_id) ?? []),
          server.name,
        ]);
      }
      serverNames = names;
      deployments = listedDeployments;
      bareServers = current.filter(
        (server) => !server.kit_deployment_id && Boolean(server.worker_id),
      );
      preferredServer = preferredServerId
        ? current.find((server) => server.id === preferredServerId)
        : undefined;
      if (
        newWhenNoTargets &&
        !untrack(() => destination) &&
        listedDeployments.length === 0 &&
        bareServers.length === 0
      ) {
        choose("new", true);
      }
      fetched = true;
    } catch {
      if (!attempt.signal.aborted) {
        // A new server stays possible while the targets cannot be read.
        failed = true;
        fetched = true;
        targetsChecked = true;
        candidates = [];
        selected = "new";
      }
    }
  }

  async function checkTargets() {
    const attempt = controller ?? new AbortController();
    controller = attempt;
    checking = true;
    try {
      const next: Candidate[] = [];
      // Each feasibility check invokes the pinned StackKits CLI. Keep those
      // checks sequential rather than fanning out expensive server work.
      for (const deployment of deployments) {
        if (attempt.signal.aborted) return;
        const selection = resolvedConfig
          ? buildLayoutSelection(resolvedConfig, deployment.id, "add-use-case")
          : null;
        if (!selection) {
          next.push({ deployment });
          continue;
        }
        try {
          const view = await previewLayoutRecommendation(selection, {
            signal: attempt.signal,
          });
          next.push({ deployment, view });
        } catch {
          if (attempt.signal.aborted) return;
          next.push({ deployment, unavailable: true });
        }
      }
      if (attempt.signal.aborted) return;
      candidates = next;
      // Eligibility comes from verified backend facts, never from a client
      // capacity score or a guessed server status. The operator may change it.
      // Prefer verified plans that leave existing workloads in place; ties
      // prefer fewer receiving nodes. Both facts come from the planner.
      const cost = (candidate: Candidate) => {
        const view = candidate.view;
        if (!view) return [0, 0];
        const moved = (view.existing ?? []).filter((existing) =>
          view.assignments.some(
            (assignment) =>
              assignment.workload === existing.workload &&
              assignment.node_id !== existing.node_id,
          ),
        ).length;
        return [
          moved,
          new Set(view.assignments.map((assignment) => assignment.node_id))
            .size,
        ];
      };
      // Without selected use cases nothing is verified yet: the StackKits keep
      // the order the homelab lists them in.
      const eligible = next.filter(compatible);
      if (hasGoals)
        eligible.sort((a, b) => {
          const [aMoved, aNodes] = cost(a);
          const [bMoved, bNodes] = cost(b);
          return (
            aMoved - bMoved ||
            aNodes - bNodes ||
            a.deployment.id.localeCompare(b.deployment.id)
          );
        });
      // The server the owner started from wins when it can take the selection.
      const preferred = preferredServer;
      const preferredTarget = !preferred
        ? ""
        : preferred.kit_deployment_id
          ? eligible.some(
              (entry) => entry.deployment.id === preferred.kit_deployment_id,
            )
            ? preferred.kit_deployment_id
            : ""
          : bareServers.some(
                (server) => server.id === preferred.id && installable(server),
              )
            ? `server:${preferred.id}`
            : "";
      const existingServer = bareServers.find(installable);
      selected =
        preferredTarget ||
        eligible[0]?.deployment.id ||
        (existingServer ? `server:${existingServer.id}` : "new");
      preferredSkipped = !preferredServerId || preferredTarget
        ? null
        : !preferred
          ? { key: "placement.preferred.missing" }
          : {
              key: preferred.kit_deployment_id
                ? "placement.preferred.incompatible"
                : "placement.preferred.notReady",
              name: preferred.name,
            };
      targetsChecked = true;
    } finally {
      checking = false;
    }
  }
</script>

{#if destination && needsResolution && !fetched}
  <section class="mx-auto max-w-6xl p-6 md:p-8">
    <LoadingNote label={tr("placement.loading")} />
  </section>
{:else if destination === "new"}
  <div class="px-6 pt-6">
    <Button variant="ghost" onclick={changeTarget} testId="use-case-target-back"
      >{tr("placement.change")}</Button
    >
  </div>
  <ConfigurationFlow
    initialConfig={resolvedConfig}
    {selfDisclosure}
    {onSelfDisclosureSettled}
  />
{:else if destination?.startsWith("server:")}
  <div class="px-6 pt-6">
    <Button variant="ghost" onclick={changeTarget} testId="use-case-target-back"
      >{tr("placement.change")}</Button
    >
  </div>
  <ConfigurationFlow
    initialConfig={resolvedConfig}
    existingServerId={destination.slice(7)}
    {selfDisclosure}
    {onSelfDisclosureSettled}
  />
{:else if destination}
  <div class="px-6 pt-6">
    <Button variant="ghost" onclick={changeTarget} testId="use-case-target-back"
      >{tr("placement.change")}</Button
    >
  </div>
  <AddUseCasesFlow stackId={destination} initialConfig={resolvedConfig} />
{:else}
  <section
    class="mx-auto max-w-6xl space-y-5 p-6 md:p-8"
    data-testid="use-case-placement"
    data-has-use-cases={hasGoals}
  >
    <header class="space-y-2">
      <h1 class="text-2xl font-semibold tracking-tight text-foreground">
        {hasGoals ? tr("placement.title") : tr("placement.chooseTitle")}
      </h1>
      <p class="max-w-prose text-sm text-muted-foreground">
        {hasGoals ? tr("placement.body") : tr("placement.chooseBody")}
      </p>
    </header>
    {#if !targetsChecked}
      <LoadingNote label={tr("placement.loading")} />
    {:else}
      {#if preferredSkipped}
        <p
          class="rounded-lg border border-warning/30 bg-warning/10 px-4 py-3 text-sm text-foreground"
          role="status"
          data-testid="use-case-preferred-skipped"
        >
          {tr(preferredSkipped.key, { name: preferredSkipped.name ?? "" })}
        </p>
      {/if}
      {#if failed}
        <ErrorCallout
          message={tr("placement.failed")}
          onRetry={load}
          testId="use-case-target-error"
        />
      {/if}
      <fieldset class="space-y-3">
        <legend class="sr-only">
          {hasGoals ? tr("placement.title") : tr("placement.chooseTitle")}
        </legend>
        {#each candidates as candidate (candidate.deployment.id)}
          {@const eligible = compatible(candidate)}
          <label
            class="flex gap-3 rounded-xl border p-4 {selected ===
            candidate.deployment.id
              ? 'border-primary bg-primary/5'
              : 'border-border'}"
            data-testid="use-case-target"
            data-stack-id={candidate.deployment.id}
            data-compatible={eligible}
          >
            <input
              type="radio"
              name="use-case-target"
              value={candidate.deployment.id}
              bind:group={selected}
              disabled={!eligible}
              class="mt-1"
            />
            <span class="min-w-0 space-y-1">
              <span class="block font-medium text-foreground"
                >{candidate.deployment
                  .name}{#if candidate.deployment.stackkit_id}<span
                    class="ms-2 text-sm font-normal text-muted-foreground"
                    >{candidate.deployment.stackkit_id}</span
                  >{/if}</span
              >
              <span class="block text-sm text-muted-foreground"
                >{candidate.view?.nodes.map((node) => node.name).join(", ") ||
                  serverNames.get(candidate.deployment.id)?.join(", ") ||
                  tr("placement.unknownNodes")}</span
              >
              <span class="block text-sm text-muted-foreground"
                >{!candidate.view && eligible
                  ? tr("placement.chooseNext")
                  : eligible
                    ? tr("placement.compatible")
                    : tr("placement.unavailable")}</span
              >
            </span>
          </label>
          {#if candidate.view && (!eligible || selected === candidate.deployment.id)}
            <LayoutRecommendationPanel
              state={{
                phase: "resolved",
                key: candidate.deployment.id,
                view: candidate.view,
              }}
            />
          {/if}
        {/each}
        {#each bareServers as server (server.id)}
          <label
            class="flex gap-3 rounded-xl border p-4 {selected ===
            `server:${server.id}`
              ? 'border-primary bg-primary/5'
              : 'border-border'}"
            data-testid="use-case-target-install"
            data-server-id={server.id}
          >
            <input
              type="radio"
              name="use-case-target"
              value={`server:${server.id}`}
              bind:group={selected}
              disabled={!installable(server)}
              class="mt-1"
            />
            <span class="space-y-1">
              <span class="block font-medium text-foreground"
                >{tr("placement.install", { name: server.name })}</span
              >
              <span class="block text-sm text-muted-foreground"
                >{tr(
                  server.provider.lease_id
                    ? "placement.leased"
                    : !server.mutations_allowed
                      ? "placement.restricted"
                      : installable(server)
                        ? "placement.installBody"
                        : "placement.disconnected",
                )}</span
              >
            </span>
          </label>
        {/each}
        <label
          class="flex gap-3 rounded-xl border p-4 {selected === 'new'
            ? 'border-primary bg-primary/5'
            : 'border-border'}"
          data-testid="use-case-target-new"
        >
          <input
            type="radio"
            name="use-case-target"
            value="new"
            bind:group={selected}
            class="mt-1"
          />
          <span class="space-y-1">
            <span class="block font-medium text-foreground"
              >{tr("placement.new")}</span
            >
            <span class="block text-sm text-muted-foreground"
              >{tr("placement.newBody")}</span
            >
          </span>
        </label>
      </fieldset>
      <Button
        variant="primary"
        disabled={!canContinue}
        onclick={() => choose(selected)}
        testId="use-case-target-continue">{tr("placement.continue")}</Button
      >
    {/if}
  </section>
{/if}
