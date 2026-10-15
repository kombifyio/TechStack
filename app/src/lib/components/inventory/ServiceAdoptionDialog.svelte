<script lang="ts">
  import { onMount } from "svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import Modal from "#lib/components/Modal.svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import { getJob } from "#lib/api/jobs.js";
  import {
    prepareApplicationAdoption,
    readApplicationAdoption,
    executeApplicationAdoption,
    releaseApplicationAdoption,
    type CanonicalService,
    type ApplicationAdoptionPlan,
  } from "#lib/api/services.js";
  let {
    service,
    release = false,
    onClose,
    onComplete,
  }: {
    service: CanonicalService;
    release?: boolean;
    onClose: () => void;
    onComplete: () => Promise<void>;
  } = $props();
  let plan = $state<ApplicationAdoptionPlan | null>(null);
  let busy = $state(false);
  let error = $state<string | null>(null);
  let phase = $state("preview");
  let jobId = $state("");
  const previewKey = crypto.randomUUID();
  const executeKey = crypto.randomUUID();
  let active = true;
  async function waitJob(id: string) {
    while (active) {
      const job = await getJob(id);
      phase = job.state;
      if (job.state === "completed") return;
      if (["failed", "canceled", "cancelled"].includes(job.state))
        throw new Error(job.error || tr("ui.services.adoptionFailed"));
      await new Promise((resolve) => setTimeout(resolve, 1500));
    }
  }
  async function prepare() {
    busy = true;
    try {
      const job = await prepareApplicationAdoption(service, previewKey);
      jobId = job.job_id || "";
      await waitJob(jobId);
      if (active) {
        plan = await readApplicationAdoption(service.id, jobId);
        phase = "review";
      }
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }
  async function execute() {
    busy = true;
    error = null;
    try {
      const job = release
        ? await releaseApplicationAdoption(service, executeKey)
        : await executeApplicationAdoption(service, plan!, executeKey);
      jobId = job.job_id || "";
      await waitJob(jobId);
      if (active) {
        await onComplete();
        if (active) phase = "verified";
      }
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }
  onMount(() => {
    if (!release) void prepare();
    else phase = "review";
    return () => {
      active = false;
    };
  });
</script>

<Modal
  title={release ? tr("ui.services.releaseBinding") : tr("ui.services.stackIt")}
  {onClose}
>
  <div class="space-y-4" data-testid="service-adoption-dialog">
    <p class="text-sm">{service.name}</p>
    <p class="text-sm text-muted-foreground">
      {release
        ? tr("ui.services.releasePreservesRuntime")
        : tr("ui.services.adoptionScope")}
    </p>
    {#if plan?.assessment}
      <dl class="grid grid-cols-[auto_1fr] gap-2 text-sm">
        <dt>{tr("ui.services.adoptionSource")}</dt>
        <dd class="break-all font-mono text-xs">
          {plan.assessment.source.containerId}
        </dd>
        <dt>{tr("ui.services.adoptionData")}</dt>
        <dd class="break-all">{plan.assessment.source.configMount}</dd>
      </dl>
      <p class="text-sm">{tr("ui.services.adoptionPreserves")}</p>
      <p class="text-sm text-muted-foreground">
        {tr("ui.services.adoptionOperations")}
      </p>
    {/if}
    {#if jobId}<a
        class="text-sm text-primary underline"
        href={`/jobs/${encodeURIComponent(jobId)}`}
        >{tr("ui.services.adoptionProgress")}</a
      >{/if}
    {#if busy && phase !== "verified"}<p role="status">
        {tr("ui.services.adoptionChecking")} ({phase})
      </p>{/if}
    {#if error}<p class="text-sm text-destructive" role="alert">{error}</p>{/if}
    {#if phase === "verified"}<p role="status">
        {release
          ? tr("ui.services.bindingReleased")
          : tr("ui.services.adoptionVerified")}
      </p>{/if}
  </div>
  {#snippet footer()}
    <Button variant="secondary" onclick={onClose}
      >{tr("ui.importExportModal.cancel")}</Button
    >
    {#if phase === "review" && !error}<Button
        variant="primary"
        onclick={execute}
        disabled={busy}
        testId="approve-service-adoption"
        >{release
          ? tr("ui.services.releaseBinding")
          : tr("ui.services.approveStackIt")}</Button
      >{/if}
  {/snippet}
</Modal>
