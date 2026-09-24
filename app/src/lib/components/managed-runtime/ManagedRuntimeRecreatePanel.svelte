<script lang="ts">
  import { goto } from "$app/navigation";
  import { parseApiError } from "#lib/api/errors.js";
  import {
    getMonthlyRuntimeCleanupReadback,
    recreateMonthlyRuntime,
    type MonthlyRuntimeCleanupReadback,
  } from "#lib/api/stacks.js";
  import { confirmInApp } from "#lib/dialogs/in-app-dialog.js";
  import {
    getOrCreateStackActionIdempotency,
    settleStackActionIdempotency,
  } from "#lib/idempotency/managed-runtime.js";
  import Button from "#lib/components/ui/Button.svelte";
  import { RefreshCw, RotateCcw } from "@lucide/svelte";

  let {
    stackId,
    leaseId,
    serverName = "managed server",
    refreshToken = 0,
  }: {
    stackId: string;
    leaseId: string;
    serverName?: string;
    refreshToken?: number;
  } = $props();

  let cleanup = $state<MonthlyRuntimeCleanupReadback | null>(null);
  let loading = $state(false);
  let recreating = $state(false);
  let error = $state<string | null>(null);

  const cleanupStarted = $derived(Boolean(cleanup?.lease.desired_terminal));
  const recreateReady = $derived(
    Boolean(
      cleanup?.lease.desired_terminal &&
      cleanup.lease.observed_terminal &&
      cleanup.server.bound &&
      cleanup.server.terminal &&
      cleanup.provider_operation.found &&
      cleanup.provider_operation.terminal &&
      cleanup.provider_operation.absence_evidence_ref &&
      cleanup.provider_operation.capacity_released,
    ),
  );

  $effect(() => {
    leaseId;
    refreshToken;
    void loadCleanup();
  });

  $effect(() => {
    if (!cleanupStarted || recreateReady || loading || recreating) return;
    const timer = window.setTimeout(() => void loadCleanup(), 5_000);
    return () => window.clearTimeout(timer);
  });

  async function loadCleanup() {
    if (!leaseId) return;
    loading = true;
    error = null;
    try {
      cleanup = await getMonthlyRuntimeCleanupReadback(leaseId);
    } catch (err) {
      cleanup = null;
      error = parseApiError(err).message;
    } finally {
      loading = false;
    }
  }

  async function recreate() {
    if (!recreateReady || recreating || !stackId || !leaseId) return;
    const confirmed = await confirmInApp({
      title: `Recreate ${serverName}?`,
      message:
        "This provisions and bills a new managed server generation. Techstack will re-establish enrollment, endpoints, Guard evidence, and the StackKit rollout through the normal creation flow.",
      confirmText: "Recreate server",
      tone: "danger",
    });
    if (!confirmed) return;

    recreating = true;
    error = null;
    try {
      const attempt = getOrCreateStackActionIdempotency(
        sessionStorage,
        `${stackId}:${leaseId}`,
        "recreate",
      );
      const result = await recreateMonthlyRuntime(leaseId, attempt.key);
      settleStackActionIdempotency(sessionStorage, attempt, { status: 202 });
      const query = new URLSearchParams({
        stack_id: result.kit_deployment_id,
        phase: "rollout",
      });
      if (result.job_id) query.set("job_id", result.job_id);
      await goto(`/stacks/creating?${query.toString()}`);
    } catch (err) {
      error = parseApiError(err).message;
    } finally {
      recreating = false;
    }
  }
</script>

{#if cleanupStarted || loading || error}
  <div
    data-kx="plate"
    class="rounded-lg border border-warning/40 bg-warning/5 p-5"
    data-testid="managed-runtime-recreate-panel"
  >
    <div
      class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between"
    >
      <div class="min-w-0">
        <div class="flex items-center gap-2">
          <RotateCcw class="h-5 w-5 text-warning" />
          <h2 class="text-lg font-semibold text-foreground">Recreate server</h2>
        </div>
        {#if loading}
          <p class="mt-2 text-sm text-muted-foreground">
            Checking terminal provider cleanup…
          </p>
        {:else if recreateReady}
          <p class="mt-2 max-w-3xl text-sm text-muted-foreground">
            Provider absence and capacity release are verified for the old
            generation. Recreate provisions a new generation through the full
            Creation screen; it does not restart the old server.
          </p>
        {:else if cleanupStarted}
          <p class="mt-2 max-w-3xl text-sm text-muted-foreground">
            Cleanup is still in progress. Recreate unlocks only after the lease,
            canonical server, provider operation, absence evidence, and capacity
            release are all terminal.
          </p>
        {/if}
        {#if error}
          <p class="mt-2 text-sm text-destructive" role="alert">{error}</p>
        {/if}
      </div>
      <div class="flex shrink-0 flex-wrap gap-2">
        {#if error || (cleanupStarted && !recreateReady)}
          <Button
            variant="secondary"
            testId="managed-runtime-recreate-refresh"
            onclick={loadCleanup}
            disabled={loading || recreating}
          >
            <RefreshCw class="h-4 w-4" />
            {loading ? "Refreshing…" : "Refresh"}
          </Button>
        {/if}
        {#if cleanupStarted}
          <Button
            variant="primary"
            testId="managed-runtime-recreate-button"
            onclick={recreate}
            disabled={!recreateReady || loading || recreating}
          >
            <RotateCcw class="h-4 w-4" />
            {recreating ? "Recreating…" : "Recreate server"}
          </Button>
        {/if}
      </div>
    </div>
  </div>
{/if}
