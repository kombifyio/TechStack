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
  import { EasyWizard } from "#lib/components/wizard/index.js";
  import { getKitDeployment } from "#lib/api/stacks.js";
  import { parseApiError } from "#lib/api/errors.js";
  import {
    createWizardRun,
    type WizardRunResponse,
  } from "#lib/api/wizardRuns.js";
  import {
    layoutErrorReasonCode,
    layoutReplacementFromError,
    type LayoutRecommendationAcceptance,
    type LayoutRecommendationView,
  } from "#lib/api/layout.js";
  import {
    buildExtendRunRequest,
    createExpansionConfig,
    WizardInputError,
    type StackConfig,
  } from "#lib/wizard/index.js";
  import {
    clearExtendWizardIdempotencyKey,
    mintExtendWizardIdempotencyKey,
  } from "#lib/wizard/idempotency-keys.js";
  import { START_PAGE_HREF } from "#lib/navigation.js";
  import { tr } from "#lib/i18n.svelte.js";

  interface Props {
    stackId: string;
    /** Use cases to start with, from a deep link; canonical ids only. */
    initialGoals?: string[];
    /** Remove page-owned chrome and spacing when mounted by a host. */
    embedded?: boolean;
  }

  let { stackId, embedded = false, initialGoals = [] }: Props = $props();

  interface Notice {
    tone: "warning" | "error" | "info";
    title: string;
    body: string;
    details?: string;
  }

  // The deep link seeds the first selection only; later edits are the owner's.
  let config = $state<StackConfig>(
    createExpansionConfig(untrack(() => initialGoals)),
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
      const request = buildExtendRunRequest(
        wizardConfig,
        stackId,
        homelabName,
        extras?.layoutRecommendation,
      );
      // The same request keeps its key (a retry replays the attempt); a
      // changed selection or recommendation gets a new one.
      const key = mintExtendWizardIdempotencyKey(
        stackId,
        JSON.stringify(request),
      );
      result = await createWizardRun(request, key);
      clearExtendWizardIdempotencyKey(stackId);
    } catch (err) {
      notice = noticeFor(err);
    } finally {
      submitting = false;
    }
  }

  const progressHref = $derived.by(() => {
    if (!result?.job_id) return "";
    const query = [
      `stack_id=${encodeURIComponent(stackId)}`,
      `job_id=${encodeURIComponent(result.job_id)}`,
    ];
    const name = result.name || homelabName;
    if (name) query.push(`name=${encodeURIComponent(name)}`);
    return `/stacks/creating?${query.join("&")}`;
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
      <div class="mt-4 flex flex-wrap gap-2">
        {#if result.state === "provisioning" && progressHref}
          <a
            href={progressHref}
            data-kx="control"
            data-variant="primary"
            class="inline-flex items-center rounded-xl px-4 py-2 text-sm font-medium"
            data-testid="add-use-cases-progress"
          >
            {tr("layout.addUseCases.provisioning.progress")}
          </a>
        {/if}
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
    <EasyWizard
      initialConfig={config}
      oncreate={addUseCases}
      isDeploying={submitting}
      submitLabel={tr("layout.addUseCases.submit")}
      submittingLabel={tr("layout.addUseCases.submitting")}
      applyDeploymentLaneDefaults={false}
      reuseExistingOwner={true}
      joinExistingDeployment={true}
      inheritServer={true}
      requireGoal={true}
      layoutStackId={stackId}
      layoutFlow="add-use-case"
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
    </EasyWizard>
  {/if}
</div>
