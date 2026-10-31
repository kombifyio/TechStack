<!--
  Add use cases to an existing homelab (Unifier Layout Planner, phase L0).

  Reuses the creation wizard on its Goals step only: the server is the
  homelab's own (inherited), nothing else is asked. The wizard shows the
  recommended distribution of the selection; pressing "Add use cases" submits
  an extend run that accepts it. If the homelab changed since the distribution
  was shown, the run stops, nothing is changed, and the updated recommendation
  is shown to be confirmed again.
-->
<script lang="ts">
  import { onMount, untrack } from "svelte";
  import ErrorCallout from "#lib/components/ui/ErrorCallout.svelte";
  import LoadingNote from "#lib/components/ui/LoadingNote.svelte";
  import { CreationPageFrame } from "#lib/components/wizard/index.js";
  import { getKitDeployment } from "#lib/api/stacks.js";
  import { parseApiError } from "#lib/api/errors.js";
  import type { LayoutRecommendationAcceptance } from "#lib/api/layout.js";
  import { AddUseCasesRun } from "#lib/creation/add-use-cases.svelte.js";
  import { createExpansionConfig, type StackConfig } from "#lib/wizard/index.js";
  import { tr } from "#lib/i18n.svelte.js";
  import UseCasesAddedResult from "./UseCasesAddedResult.svelte";

  interface Props {
    stackId: string;
    /** Use cases to start with, from a deep link; canonical ids only. */
    initialGoals?: string[];
    /** Preserve the guided answers handed over with the selected use case. */
    initialConfig?: StackConfig;
    /** Remove page-owned chrome and spacing when mounted by a host. */
    embedded?: boolean;
  }

  let {
    stackId,
    embedded = false,
    initialGoals = [],
    initialConfig,
  }: Props = $props();

  const run = new AddUseCasesRun();

  // The deep link seeds the first selection only; later edits are the owner's.
  let config = $state<StackConfig>(
    untrack(() => {
      const expansion = createExpansionConfig(initialGoals);
      return initialConfig
        ? {
            ...expansion,
            goals: initialConfig.goals,
            useCaseSetupAnswers: initialConfig.useCaseSetupAnswers,
            useCaseSettings: initialConfig.useCaseSettings,
            useCaseCapabilities: initialConfig.useCaseCapabilities,
          }
        : expansion;
    }),
  );
  let homelabName = $state("");
  let loading = $state(true);
  let loadError = $state<string | null>(null);

  onMount(() => {
    void load();
  });

  async function load() {
    loading = true;
    loadError = null;
    try {
      const deployment = await getKitDeployment(stackId);
      homelabName = deployment?.name ?? "";
    } catch (err) {
      loadError =
        parseApiError(err).message ||
        tr("ui.managedCreationFlow.stackkitDeploymentNotFound");
    } finally {
      loading = false;
    }
  }

  function addUseCases(
    wizardConfig: StackConfig,
    extras?: { layoutRecommendation?: LayoutRecommendationAcceptance },
  ) {
    void run.submit(
      { stackId, stackName: homelabName },
      wizardConfig,
      extras?.layoutRecommendation,
    );
  }
</script>

<div
  class={embedded ? "w-full p-6" : "mx-auto max-w-6xl p-6 md:p-8"}
  data-testid="add-use-cases-page"
  data-presentation={embedded ? "embedded" : "page"}
>
  {#if loading}
    <LoadingNote label={tr("common.loading")} />
  {:else if loadError}
    <ErrorCallout message={loadError} testId="add-use-cases-error" />
  {:else if run.result}
    <UseCasesAddedResult
      result={run.result}
      {stackId}
      progressHref={run.progressHref(stackId, homelabName)}
      {embedded}
    />
  {:else}
    <header class="mb-6 space-y-1">
      <h1 class="text-2xl font-semibold tracking-tight text-foreground">
        {tr("layout.addUseCases.title")}
      </h1>
      <p class="max-w-prose text-sm text-muted-foreground">
        {tr("layout.addUseCases.subtitle")}
      </p>
    </header>
    <CreationPageFrame
      process={run.process}
      initialConfig={config}
      oncreate={addUseCases}
      isDeploying={run.submitting}
      submitLabel={tr("layout.addUseCases.submit")}
      submittingLabel={tr("layout.addUseCases.submitting")}
      layoutStackId={stackId}
      layoutReplacement={run.replacement}
      layoutRefresh={run.refresh}
    >
      {#snippet footerNotice()}
        {#if run.notice}
          <ErrorCallout
            tone={run.notice.tone}
            title={run.notice.title}
            message={run.notice.body}
            details={run.notice.details ?? null}
            testId="add-use-cases-notice"
          />
        {/if}
      {/snippet}
    </CreationPageFrame>
  {/if}
</div>
