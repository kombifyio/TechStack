<script lang="ts">
  import { onMount } from "svelte";
  import { getJob, type JobDetail } from "#lib/api/jobs.js";
  import { tr } from "#lib/i18n.svelte.js";
  import ErrorCallout from "#lib/components/ui/ErrorCallout.svelte";
  import { emitCreationEvent } from "#lib/creation/host.js";

  let {
    stackId,
    jobIds,
    runId = "",
  }: { stackId: string; jobIds: string[]; runId?: string } = $props();
  const ids = $derived([...new Set(jobIds.filter(Boolean))]);
  let jobs = $state<Record<string, JobDetail>>({});
  let unavailable = $state(false);
  const succeeded = (job: JobDetail | undefined) =>
    job?.state === "completed" || job?.state === "success";
  const failed = (job: JobDetail | undefined) =>
    ["failed", "error", "canceled", "cancelled"].includes(job?.state ?? "");
  const finished = $derived(
    ids.length > 0 &&
      ids.every((id) => succeeded(jobs[id]) || failed(jobs[id])),
  );
  const complete = $derived(
    ids.length > 0 && ids.every((id) => succeeded(jobs[id])),
  );
  const needsAttention = $derived(ids.some((id) => failed(jobs[id])));

  // The host hears the same state this view shows, once per change.
  const phase = $derived(
    ids.map((id) => jobs[id]?.step || jobs[id]?.state).find(Boolean) ?? "",
  );
  $effect(() => {
    if (finished) {
      emitCreationEvent({
        event: "completed",
        runId,
        stackId,
        outcome: complete ? "succeeded" : "failed",
      });
    } else if (phase) {
      emitCreationEvent({ event: "progress", runId, phase });
    }
  });

  onMount(() => {
    let disposed = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    async function refresh() {
      const current = await Promise.allSettled(ids.map((id) => getJob(id)));
      if (disposed) return;
      let missing = false;
      for (let index = 0; index < ids.length; index++) {
        const result = current[index];
        if (
          result?.status !== "fulfilled" ||
          result.value.kit_deployment_id !== stackId
        ) {
          missing = true;
          continue;
        }
        jobs[ids[index]] = result.value;
      }
      unavailable = missing;
      if (!finished || missing) timer = setTimeout(refresh, 5_000);
    }
    void refresh();
    return () => {
      disposed = true;
      if (timer) clearTimeout(timer);
    };
  });
</script>

<div
  class="mt-4 space-y-3"
  data-testid="use-case-run-progress"
  data-complete={complete}
  data-failed={needsAttention}
>
  <p role="status" class="text-sm text-foreground">
    {tr(
      complete
        ? "useCaseRun.complete"
        : needsAttention
          ? "useCaseRun.failed"
          : "useCaseRun.running",
    )}
  </p>
  {#if unavailable || ids.length === 0}
    <ErrorCallout
      message={tr("useCaseRun.unavailable")}
      testId="use-case-run-unavailable"
    />
  {/if}
  <ul class="space-y-2">
    {#each ids as id (id)}
      {@const job = jobs[id]}
      <li
        class="text-sm"
        data-testid="use-case-run-job"
        data-job-id={id}
        data-state={job?.state ?? "unknown"}
      >
        <span class="font-medium"
          >{tr(
            succeeded(job)
              ? "useCaseRun.done"
              : failed(job)
                ? "useCaseRun.attention"
                : "useCaseRun.pending",
          )}</span
        >
        {#if job?.message || job?.step}<span class="text-muted-foreground">
            — {job.message || job.step}</span
          >{/if}
        {#if failed(job) && job?.error}<p class="mt-1 text-destructive">
            {job.error}
          </p>{/if}
      </li>
    {/each}
  </ul>
</div>
