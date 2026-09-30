<!--
  WizardRunBanner - the wizard run's notice. It renders at the one Node the
  run's rollout belongs to (a compact card at the top only while the homelab
  has no Node at all). The run ledger (GET /api/v1/wizard/runs/active) is the
  source, so it survives cleared sessionStorage and other devices. The owner
  can resume, cancel the rollout (confirmed) or dismiss it server-side.
-->
<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import {
    cancelWizardRunRollout,
    dismissWizardRun,
    type ActiveWizardRun,
  } from "#lib/api/wizardRuns.js";
  import { confirmInApp } from "#lib/dialogs/in-app-dialog.js";
  import { Loader2, TriangleAlert, Cable } from "@lucide/svelte";

  interface Props {
    run: ActiveWizardRun;
    /** Called after a cancel or dismiss so the page re-reads the run. */
    onChanged?: () => void;
  }

  let { run, onChanged }: Props = $props();

  let busy = $state<"cancel" | "dismiss" | null>(null);
  let actionError = $state<string | null>(null);

  const jobLive = $derived(
    ["pending", "running", "waiting", "in_progress"].includes(
      run.job?.state ?? "",
    ),
  );
  const canCancel = $derived(
    jobLive && Boolean(run.kit_deployment_id) && Boolean(run.job?.id),
  );

  function errorText(error: unknown, fallbackKey: string): string {
    const message = error instanceof Error ? error.message.trim() : "";
    return message ? `${tr(fallbackKey)}: ${message}` : tr(fallbackKey);
  }

  async function cancelRollout() {
    const confirmed = await confirmInApp({
      title: tr("wizard.run.banner.cancelTitle"),
      message: tr("wizard.run.banner.cancelBody"),
      confirmText: tr("wizard.run.banner.cancel"),
      tone: "danger",
    });
    if (!confirmed) return;
    busy = "cancel";
    actionError = null;
    try {
      await cancelWizardRunRollout(run);
      onChanged?.();
    } catch (error) {
      actionError = errorText(error, "wizard.run.banner.cancelFailed");
    } finally {
      busy = null;
    }
  }

  async function dismiss() {
    busy = "dismiss";
    actionError = null;
    try {
      await dismissWizardRun(run.run_id);
      onChanged?.();
    } catch (error) {
      actionError = errorText(error, "wizard.run.banner.dismissFailed");
    } finally {
      busy = null;
    }
  }

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
      return run.result?.kit_assignment_mode === "join" && run.kit_deployment_id
        ? `/stacks/${encodeURIComponent(run.kit_deployment_id)}/servers/new`
        : "/stacks/new";
    }
    const params = new URLSearchParams();
    params.set("job_id", jobRef);
    if (run.kit_deployment_id) {
      params.set("stack_id", run.kit_deployment_id);
    }
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
  class={`rounded-lg border p-3 ${
    failed
      ? "border-warning/30 bg-warning/10"
      : "border-primary/30 bg-primary/5"
  }`}
  data-testid="wizard-run-banner"
  role="status"
>
  <div class="flex flex-wrap items-center gap-3">
    <div class="shrink-0">
      {#if failed}
        <TriangleAlert class="h-4 w-4 text-warning" />
      {:else if awaitingPairing}
        <Cable class="h-4 w-4 text-primary" />
      {:else}
        <Loader2 class="h-4 w-4 animate-spin text-primary" />
      {/if}
    </div>
    <div class="min-w-0 flex-1">
      <p class="text-sm font-medium text-foreground">{tr(titleKey)}</p>
      <p class="text-xs text-muted-foreground">
        {jobMessage || tr(bodyKey)}
      </p>
      {#if !failed && !awaitingPairing && progress > 0}
        <div class="mt-1.5 h-1 w-full max-w-xs rounded-full bg-muted">
          <div
            class="h-1 rounded-full bg-primary transition-all"
            style={`width: ${Math.min(progress, 100)}%`}
          ></div>
        </div>
      {/if}
    </div>
    <div class="flex shrink-0 flex-wrap items-center gap-2">
      <a
        data-kx="control"
        data-variant="primary"
        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium"
        href={resumeHref}
        data-testid="wizard-run-banner-resume"
      >
        {tr("wizard.run.banner.resume")}
      </a>
      {#if canCancel}
        <button
          type="button"
          data-kx="control"
          data-variant="secondary"
          class="inline-flex items-center justify-center whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium text-destructive disabled:pointer-events-none disabled:opacity-50"
          data-testid="wizard-run-banner-cancel"
          disabled={busy !== null}
          onclick={cancelRollout}
        >
          {tr("wizard.run.banner.cancel")}
        </button>
      {/if}
      <button
        type="button"
        data-kx="control"
        data-variant="ghost"
        class="inline-flex items-center justify-center whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium text-muted-foreground disabled:pointer-events-none disabled:opacity-50"
        data-testid="wizard-run-banner-dismiss"
        disabled={busy !== null}
        onclick={dismiss}
      >
        {tr("wizard.run.banner.dismiss")}
      </button>
    </div>
  </div>
  {#if actionError}
    <p class="mt-2 text-xs text-destructive" role="alert">{actionError}</p>
  {/if}
</div>
