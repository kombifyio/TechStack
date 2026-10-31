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
  import UseCaseRunProgress from "./UseCaseRunProgress.svelte";
  import { onMount, untrack } from "svelte";
  import ErrorCallout from "#lib/components/ui/ErrorCallout.svelte";
  import LoadingNote from "#lib/components/ui/LoadingNote.svelte";
  import { CreationPageFrame } from "#lib/components/wizard/index.js";
  import { getKitDeployment } from "#lib/api/stacks.js";
  import { parseApiError } from "#lib/api/errors.js";
  import type { WizardRunResponse } from "#lib/api/wizardRuns.js";
  import { CREATION_PROCESSES } from "#lib/creation/core/index.js";
  import { creationScreenHref, submitCreationRun } from "#lib/creation/submit.js";
  import {
    layoutErrorReasonCode,
    layoutReplacementFromError,
    type LayoutRecommendationAcceptance,
    type LayoutRecommendationView,
  } from "#lib/api/layout.js";
  import {
    createExpansionConfig,
    WizardInputError,
    type StackConfig,
  } from "#lib/wizard/index.js";
  import { START_PAGE_HREF } from "#lib/navigation.js";
  import { emitCreationEvent } from "#lib/creation/host.js";
  import { tr } from "#lib/i18n.svelte.js";

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

  const process = CREATION_PROCESSES["add-use-case"];

  interface Notice {
    tone: "warning" | "error" | "info";
    title: string;
    body: string;
    details?: string;
  }

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
  let submitting = $state(false);
  let result = $state<WizardRunResponse | null>(null);
  let notice = $state<Notice | null>(null);
  let replacement = $state<LayoutRecommendationView | undefined>();
  let refresh = $state(0);

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

  function noticeFor(err: unknown): Notice {
    if (err instanceof WizardInputError) {
      return {
        tone: "warning",
        title: tr("layout.addUseCases.failed.title"),
        body: err.message,
      };
    }
    switch (layoutErrorReasonCode(err)) {
      case "layout_recommendation_superseded":
        replacement = layoutReplacementFromError(err);
        // Without a replacement in the answer, ask for the recommendation again.
        if (!replacement) refresh += 1;
        return {
          tone: "warning",
          title: tr("layout.addUseCases.superseded.title"),
          body: tr("layout.addUseCases.superseded.body"),
        };
      case "layout_recommendation_stale":
      case "layout_recommendation_unverifiable":
        replacement = undefined;
        refresh += 1;
        return {
          tone: "warning",
          title: tr("layout.addUseCases.stale.title"),
          body: tr("layout.addUseCases.stale.body"),
        };
      case "layout_recommendation_unavailable":
      case "layout_recommendations_unavailable":
        return {
          tone: "info",
          title: tr("layout.addUseCases.unavailable.title"),
          body: tr("layout.addUseCases.unavailable.body"),
        };
      default:
        return {
          tone: "error",
          title: tr("layout.addUseCases.failed.title"),
          body: tr("layout.addUseCases.failed.body"),
          details: parseApiError(err).message,
        };
    }
  }

  async function addUseCases(
    wizardConfig: StackConfig,
    extras?: { layoutRecommendation?: LayoutRecommendationAcceptance },
  ) {
    submitting = true;
    notice = null;
    try {
      ({ run: result } = await submitCreationRun(process, wizardConfig, {
        stackId,
        stackName: homelabName,
        layoutRecommendation: extras?.layoutRecommendation,
      }));
    } catch (err) {
      notice = noticeFor(err);
    } finally {
      submitting = false;
    }
  }

  // Saved without a rollout: nothing further will report to the host.
  $effect(() => {
    if (result?.state === "accepted")
      emitCreationEvent({
        event: "completed",
        runId: result.run_id,
        stackId,
        outcome: "accepted",
      });
  });

  const progressHref = $derived.by(() => {
    // The inline group view follows every incremental node operation.
    // The creation page follows one provisioning job only.
    if (result?.job_ids?.length) return "";
    if (!result?.job_id) return "";
    return creationScreenHref(
      process,
      { ...result, kit_deployment_id: stackId },
      result.name || homelabName,
    );
  });
</script>

<div
  class={embedded ? "w-full" : "mx-auto max-w-6xl p-6 md:p-8"}
  data-testid="add-use-cases-page"
  data-presentation={embedded ? "embedded" : "page"}
>
  {#if loading}
    <LoadingNote label={tr("common.loading")} />
  {:else if loadError}
    <ErrorCallout message={loadError} testId="add-use-cases-error" />
  {:else if result}
    <section
      class="mx-auto max-w-2xl rounded-xl border border-success/30 bg-success/5 p-5"
      data-testid="add-use-cases-result"
      data-run-state={result.state}
      role="status"
    >
      <h1 class="text-lg font-semibold text-foreground">
        {result.state === "provisioning"
          ? tr("layout.addUseCases.provisioning.title")
          : tr("layout.addUseCases.accepted.title")}
      </h1>
      <p class="mt-1 text-sm text-muted-foreground">
        {result.state === "provisioning"
          ? tr("layout.addUseCases.provisioning.body")
          : tr("layout.addUseCases.accepted.body")}
      </p>
      {#if result.state === "provisioning"}
        <UseCaseRunProgress
          {stackId}
          jobIds={result.job_ids ?? (result.job_id ? [result.job_id] : [])}
          runId={result.run_id}
        />
      {/if}
      <div class="mt-4 flex flex-wrap gap-2">
        {#if result.state === "provisioning" && progressHref}
          <a
            href={progressHref}
            data-kx="control"
            data-variant="secondary"
            class="inline-flex items-center rounded-xl px-4 py-2 text-sm font-medium"
            data-testid="add-use-cases-progress"
          >
            {tr("layout.addUseCases.provisioning.progress")}
          </a>
        {/if}
        {#if !embedded}
          <a
            href={START_PAGE_HREF}
            data-kx="control"
            data-variant={result.state === "provisioning" && progressHref
              ? undefined
              : "primary"}
            class="inline-flex items-center rounded-xl px-4 py-2 text-sm font-medium"
            data-testid="add-use-cases-back"
          >
            {tr("layout.addUseCases.backToHomelab")}
          </a>
        {/if}
      </div>
    </section>
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
      {process}
      initialConfig={config}
      oncreate={addUseCases}
      isDeploying={submitting}
      submitLabel={tr("layout.addUseCases.submit")}
      submittingLabel={tr("layout.addUseCases.submitting")}
      layoutStackId={stackId}
      layoutReplacement={replacement}
      layoutRefresh={refresh}
    >
      {#snippet footerNotice()}
        {#if notice}
          <ErrorCallout
            tone={notice.tone}
            title={notice.title}
            message={notice.body}
            details={notice.details ?? null}
            testId="add-use-cases-notice"
          />
        {/if}
      {/snippet}
    </CreationPageFrame>
  {/if}
</div>
