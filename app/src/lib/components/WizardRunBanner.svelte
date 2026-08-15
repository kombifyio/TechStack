<!--
  WizardRunBanner - contextual dashboard card while a wizard run is active or
  unfinished (plan D6). Links back to the progress page; the run ledger
  (GET /api/v1/wizard/runs/active) is the source, so the banner survives
  cleared sessionStorage and other devices.
-->
<script lang="ts">
  import { tr } from "$lib/i18n.svelte";
  import type { ActiveWizardRun } from "$lib/api/wizardRuns";
  import { Loader2, TriangleAlert, Cable } from "@lucide/svelte";

  interface Props {
    run: ActiveWizardRun;
  }

  let { run }: Props = $props();

  const failed = $derived(run.status === "failed");
  const awaitingPairing = $derived(
    !failed &&
      typeof run.result?.state === "string" &&
      run.result.state === "awaiting_pairing",
  );
  const progress = $derived(run.job?.progress ?? 0);
  const jobMessage = $derived(run.job?.message ?? "");

  const titleKey = $derived(
    failed
      ? "wizard.run.banner.failedTitle"
      : awaitingPairing
        ? "wizard.run.banner.pairingTitle"
        : "wizard.run.banner.inProgressTitle",
  );
  const bodyKey = $derived(
    failed
      ? "wizard.run.banner.failedBody"
      : awaitingPairing
        ? "wizard.run.banner.pairingBody"
        : "wizard.run.banner.inProgressBody",
  );

  const resumeHref = $derived.by(() => {
    const jobRef = run.job_id || run.pairing_job_id || "";
    if (!jobRef) {
      // A run without any job reference (e.g. failed before dispatch) cannot
      // resume on the progress page; send the user back into the wizard.
      return run.result?.kit_assignment_mode === "join" && run.stack_id
        ? `/stacks/${encodeURIComponent(run.stack_id)}/servers/new`
        : "/stacks/new";
    }
    const params = new URLSearchParams();
    params.set("job_id", jobRef);
    if (run.stack_id) params.set("stack_id", run.stack_id);
    if (run.pairing_job_id && run.pairing_job_id !== jobRef) {
      params.set("pairing_job_id", run.pairing_job_id);
    }
    if (run.result?.kit_assignment_mode === "join") {
      params.set("operation", "add-server");
    }
    const resultName = run.result?.name;
    if (typeof resultName === "string" && resultName) {
      params.set("name", resultName);
    }
    return `/stacks/creating?${params.toString()}`;
  });
</script>

<div
  class={`rounded-lg border p-4 ${
    failed
      ? "border-warning/30 bg-warning/10"
      : "border-primary/30 bg-primary/5"
  }`}
  data-testid="wizard-run-banner"
>
  <div class="flex items-center gap-4">
    <div class="shrink-0">
      {#if failed}
        <TriangleAlert class="h-5 w-5 text-warning" />
      {:else if awaitingPairing}
        <Cable class="h-5 w-5 text-primary" />
      {:else}
        <Loader2 class="h-5 w-5 animate-spin text-primary" />
      {/if}
    </div>
    <div class="min-w-0 flex-1">
      <p class="font-medium text-foreground">{tr(titleKey)}</p>
      <p class="text-sm text-muted-foreground">
        {jobMessage || tr(bodyKey)}
      </p>
      {#if !failed && !awaitingPairing && progress > 0}
        <div class="mt-2 h-1.5 w-full max-w-xs rounded-full bg-muted">
          <div
            class="h-1.5 rounded-full bg-primary transition-all"
            style={`width: ${Math.min(progress, 100)}%`}
          ></div>
        </div>
      {/if}
    </div>
    <a
      class="btn btn-primary shrink-0"
      href={resumeHref}
      data-testid="wizard-run-banner-resume"
    >
      {tr("wizard.run.banner.resume")}
    </a>
  </div>
</div>
