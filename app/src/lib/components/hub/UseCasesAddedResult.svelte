<!--
  What an accepted extend run did: saved, or rolling out with live progress
  per job, and the way back to the homelab.
-->
<script lang="ts">
  import UseCaseRunProgress from "./UseCaseRunProgress.svelte";
  import type { WizardRunResponse } from "#lib/api/wizardRuns.js";
  import { START_PAGE_HREF } from "#lib/navigation.js";
  import { tr } from "#lib/i18n.svelte.js";

  let {
    result,
    stackId,
    progressHref = "",
    embedded = false,
  }: {
    result: WizardRunResponse;
    stackId: string;
    /** The Creation Screen, when the run follows one provisioning job. */
    progressHref?: string;
    embedded?: boolean;
  } = $props();
</script>

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
